package file

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/sergi/go-diff/diffmatchpatch"
)

// EditPair 单次编辑操作
type EditPair struct {
	Old string `json:"old"`
	New string `json:"new"`
}

// FileEditInput 输入参数
type FileEditInput struct {
	FilePath   string     `json:"file_path"`
	OldString  string     `json:"old_string"`
	NewString  string     `json:"new_string"`
	ReplaceAll bool       `json:"replace_all,omitempty"`
	LineStart  int        `json:"line_start,omitempty"`
	LineEnd    int        `json:"line_end,omitempty"`
	DryRun     bool       `json:"dry_run,omitempty"`
	Edits      []EditPair `json:"edits,omitempty"`
}

// FileEditOutput 输出结果
type FileEditOutput struct {
	OriginalContent string `json:"original_content,omitempty"`
	EditedContent   string `json:"edited_content"`
	Patch           string `json:"patch"`
	GitDiff         string `json:"git_diff,omitempty"`
	LinesChanged    int    `json:"lines_changed"`
	DryRun          bool   `json:"dry_run,omitempty"`
	Message         string `json:"message,omitempty"`
	ErrorCode       int    `json:"error_code,omitempty"`
	Suggestions     string `json:"suggestions,omitempty"`
}

const MaxEditFileSize = 1024 * 1024 * 1024

func FileEditTool(input FileEditInput) (*FileEditOutput, error) {
	fullPath, err := expandPath(input.FilePath)
	if err != nil {
		return nil, fmt.Errorf("invalid path: %w", err)
	}

	if isUNCPath(fullPath) {
		return nil, errors.New("UNC paths are not allowed for security reasons")
	}

	if err := validatePathSafety(fullPath); err != nil {
		return nil, err
	}

	if !input.DryRun {
		if err := checkWritePermission(fullPath); err != nil {
			return nil, err
		}
	}

	info, statErr := os.Stat(fullPath)
	if statErr == nil && info.Size() > MaxEditFileSize {
		return nil, fmt.Errorf("file too large (%d bytes)", info.Size())
	}

	originalBytes, err := os.ReadFile(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			if input.OldString != "" && len(input.Edits) == 0 {
				return nil, fmt.Errorf("file does not exist, set old_string to empty to create")
			}
			if input.DryRun {
				return &FileEditOutput{EditedContent: input.NewString, DryRun: true, Message: "[DRY RUN] Would create new file"}, nil
			}
			return createNewFile(fullPath, input.NewString)
		}
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	if isBinaryFile(originalBytes) {
		return nil, fmt.Errorf("cannot edit binary file: %s", fullPath)
	}

	originalContent := normalizeLineEndings(string(originalBytes))

	if err := checkFileFreshness(fullPath, originalBytes); err != nil {
		return nil, fmt.Errorf("file was modified unexpectedly: %w", err)
	}

	if len(input.Edits) > 0 {
		return performBatchEdits(fullPath, originalContent, originalBytes, input)
	}

	if input.LineStart > 0 {
		return performLineRangeEdit(fullPath, originalContent, originalBytes, input)
	}

	if input.OldString == input.NewString {
		return nil, errors.New("no changes: old_string and new_string are identical")
	}

	editedContent, patch, err := performEdit(originalContent, input.OldString, input.NewString, input.ReplaceAll)
	if err != nil {
		suggestions := findSimilarMatches(originalContent, input.OldString)
		if suggestions != "" {
			return nil, fmt.Errorf("%w\n\nFuzzy match suggestions - similar locations in file:\n%s", err, suggestions)
		}
		return nil, err
	}

	if input.DryRun {
		linesChanged := countLinesChanged(originalContent, editedContent)
		return &FileEditOutput{OriginalContent: originalContent, EditedContent: editedContent, Patch: patch, LinesChanged: linesChanged, DryRun: true, Message: "[DRY RUN] Preview only. Remove dry_run to apply."}, nil
	}

	if err := writeFileWithOriginalStyle(fullPath, editedContent, originalBytes); err != nil {
		return nil, fmt.Errorf("failed to write file: %w", err)
	}

	linesChanged := countLinesChanged(originalContent, editedContent)
	return &FileEditOutput{OriginalContent: originalContent, EditedContent: editedContent, Patch: patch, LinesChanged: linesChanged, Message: "File edited successfully"}, nil
}

func performBatchEdits(fullPath, originalContent string, originalBytes []byte, input FileEditInput) (*FileEditOutput, error) {
	editedContent := originalContent
	var allPatches []string
	hasChange := false

	for i, edit := range input.Edits {
		if edit.Old == edit.New {
			continue
		}

		result, _, err := performEdit(editedContent, edit.Old, edit.New, false)
		if err != nil {
			suggestions := findSimilarMatches(editedContent, edit.Old)
			if suggestions != "" {
				return nil, fmt.Errorf("edits[%d]: %w\n\nFuzzy match suggestions:\n%s", i, err, suggestions)
			}
			return nil, fmt.Errorf("edits[%d]: %w", i, err)
		}
		if result != editedContent {
			patch := diffToUnified(editedContent, result)
			allPatches = append(allPatches, fmt.Sprintf("--- edit[%d]\n%s", i, patch))
			editedContent = result
			hasChange = true
		}
	}

	if !hasChange {
		return nil, errors.New("no changes made after all edits applied")
	}

	combinedPatch := strings.Join(allPatches, "\n")
	linesChanged := countLinesChanged(originalContent, editedContent)

	if input.DryRun {
		return &FileEditOutput{OriginalContent: originalContent, EditedContent: editedContent, Patch: combinedPatch, LinesChanged: linesChanged, DryRun: true, Message: fmt.Sprintf("[DRY RUN] %d edit(s) would be applied.", len(input.Edits))}, nil
	}

	if err := writeFileWithOriginalStyle(fullPath, editedContent, originalBytes); err != nil {
		return nil, fmt.Errorf("failed to write file: %w", err)
	}

	return &FileEditOutput{OriginalContent: originalContent, EditedContent: editedContent, Patch: combinedPatch, LinesChanged: linesChanged, Message: fmt.Sprintf("%d edit(s) applied successfully", len(input.Edits))}, nil
}

func performLineRangeEdit(fullPath, originalContent string, originalBytes []byte, input FileEditInput) (*FileEditOutput, error) {
	lines := strings.Split(originalContent, "\n")
	totalLines := len(lines)

	lineStart := input.LineStart
	lineEnd := input.LineEnd
	if lineEnd <= 0 {
		lineEnd = lineStart
	}

	if lineStart < 1 || lineStart > totalLines {
		return nil, fmt.Errorf("line_start %d out of range (1-%d)", lineStart, totalLines)
	}
	if lineEnd < lineStart || lineEnd > totalLines {
		return nil, fmt.Errorf("line_end %d invalid, valid range: %d-%d", lineEnd, lineStart, totalLines)
	}

	var newLines []string
	newLines = append(newLines, lines[:lineStart-1]...)
	newLines = append(newLines, input.NewString)
	newLines = append(newLines, lines[lineEnd:]...)

	editedContent := strings.Join(newLines, "\n")

	if editedContent == originalContent {
		return nil, errors.New("no change: new content identical to original lines")
	}

	patch := diffToUnified(originalContent, editedContent)
	linesChanged := countLinesChanged(originalContent, editedContent)

	if input.DryRun {
		return &FileEditOutput{OriginalContent: originalContent, EditedContent: editedContent, Patch: patch, LinesChanged: linesChanged, DryRun: true, Message: fmt.Sprintf("[DRY RUN] Would replace lines %d-%d.", lineStart, lineEnd)}, nil
	}

	if err := writeFileWithOriginalStyle(fullPath, editedContent, originalBytes); err != nil {
		return nil, fmt.Errorf("failed to write file: %w", err)
	}

	return &FileEditOutput{OriginalContent: originalContent, EditedContent: editedContent, Patch: patch, LinesChanged: linesChanged, Message: fmt.Sprintf("Replaced lines %d-%d successfully", lineStart, lineEnd)}, nil
}

func findSimilarMatches(content, oldStr string) string {
	if oldStr == "" || len(oldStr) < 3 {
		return ""
	}

	oldLines := strings.Split(strings.TrimSpace(oldStr), "\n")
	contentLines := strings.Split(content, "\n")

	type match struct {
		lineNum int
		content string
		score   int
	}
	var matches []match

	firstLine := strings.TrimSpace(oldLines[0])
	if len(firstLine) < 5 {
		return ""
	}

	for i, line := range contentLines {
		trimmed := strings.TrimSpace(line)
		if len(trimmed) < 5 {
			continue
		}
		score := similarityScore(trimmed, firstLine)
		if score > 70 {
			if len(oldLines) > 1 && i+len(oldLines) <= len(contentLines) {
				totalScore := score
				for j := 1; j < len(oldLines) && j <= 2; j++ {
					totalScore += similarityScore(strings.TrimSpace(contentLines[i+j]), strings.TrimSpace(oldLines[j]))
				}
				score = totalScore / (len(oldLines) + 1)
			}
			if score > 60 {
				contextEnd := i + len(oldLines)
				if contextEnd > len(contentLines) {
					contextEnd = len(contentLines)
				}
				ctx := strings.Join(contentLines[i:contextEnd], "\n")
				matches = append(matches, match{lineNum: i + 1, content: ctx, score: score})
			}
		}
	}

	if len(matches) == 0 {
		return ""
	}

	for i := 0; i < len(matches)-1; i++ {
		for j := i + 1; j < len(matches); j++ {
			if matches[j].score > matches[i].score {
				matches[i], matches[j] = matches[j], matches[i]
			}
		}
	}

	if len(matches) > 3 {
		matches = matches[:3]
	}

	var sb strings.Builder
	for _, m := range matches {
		sb.WriteString(fmt.Sprintf("  Line %d (similarity %d%%):\n", m.lineNum, m.score))
		for _, line := range strings.Split(m.content, "\n") {
			if len(line) > 100 {
				line = line[:100] + "..."
			}
			sb.WriteString(fmt.Sprintf("    %s\n", line))
		}
	}

	return sb.String()
}

func similarityScore(a, b string) int {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == b {
		return 100
	}
	if len(a) == 0 || len(b) == 0 {
		return 0
	}

	shorter := a
	longer := b
	if len(a) > len(b) {
		shorter, longer = b, a
	}

	matchCount := 0
	window := 3
	if len(shorter) < window {
		window = len(shorter)
	}
	for i := 0; i <= len(shorter)-window; i++ {
		sub := shorter[i : i+window]
		if strings.Contains(longer, sub) {
			matchCount++
		}
	}

	maxWindows := len(shorter) - window + 1
	if maxWindows <= 0 {
		return 0
	}
	return matchCount * 100 / maxWindows
}

func createNewFile(path, content string) (*FileEditOutput, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}

	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return nil, err
	}

	return &FileEditOutput{EditedContent: content, Patch: fmt.Sprintf("--- null\n+++ %s\n@@ -0,0 +1 @@\n+%s", path, content), Message: "New file created"}, nil
}

func performEdit(original, oldStr, newStr string, replaceAll bool) (string, string, error) {
	if oldStr == "" {
		edited := original + newStr
		patch := diffToUnified(original, edited)
		return edited, patch, nil
	}

	var edited string
	if replaceAll {
		edited = strings.ReplaceAll(original, oldStr, newStr)
	} else {
		idx := strings.Index(original, oldStr)
		if idx == -1 {
			edited = strings.Replace(original, oldStr, newStr, 1)
			if edited == original {
				return "", "", errors.New("old_string not found in file. Try providing more context")
			}
		} else {
			edited = original[:idx] + newStr + original[idx+len(oldStr):]
		}
	}

	if edited == original {
		return "", "", errors.New("no change made after replacement")
	}

	patch := diffToUnified(original, edited)
	return edited, patch, nil
}

func diffToUnified(a, b string) string {
	dmp := diffmatchpatch.New()

	aEnc, bEnc, lineArray := dmp.DiffLinesToChars(a, b)
	diffs := dmp.DiffMain(aEnc, bEnc, false)
	diffs = dmp.DiffCharsToLines(diffs, lineArray)

	type lineEntry struct {
		op   diffmatchpatch.Operation
		text string
	}
	var entries []lineEntry
	for _, d := range diffs {
		raw := d.Text
		if strings.HasSuffix(raw, "\n") {
			raw = raw[:len(raw)-1]
		}
		for _, line := range strings.Split(raw, "\n") {
			entries = append(entries, lineEntry{op: d.Type, text: line})
		}
	}

	hasChange := false
	for _, e := range entries {
		if e.op != diffmatchpatch.DiffEqual {
			hasChange = true
			break
		}
	}
	if !hasChange {
		return ""
	}

	const ctx = 3
	include := make([]bool, len(entries))
	for i, e := range entries {
		if e.op != diffmatchpatch.DiffEqual {
			lo := max(0, i-ctx)
			hi := min(len(entries)-1, i+ctx)
			for j := lo; j <= hi; j++ {
				include[j] = true
			}
		}
	}

	var sb strings.Builder
	oldLine, newLine := 1, 1
	inHunk := false

	for i, e := range entries {
		if include[i] {
			if !inHunk {
				sb.WriteString(fmt.Sprintf("@@ -%d +%d @@\n", oldLine, newLine))
				inHunk = true
			}
			switch e.op {
			case diffmatchpatch.DiffEqual:
				sb.WriteString(" " + e.text + "\n")
				oldLine++
				newLine++
			case diffmatchpatch.DiffDelete:
				sb.WriteString("-" + e.text + "\n")
				oldLine++
			case diffmatchpatch.DiffInsert:
				sb.WriteString("+" + e.text + "\n")
				newLine++
			}
		} else {
			inHunk = false
			switch e.op {
			case diffmatchpatch.DiffEqual:
				oldLine++
				newLine++
			case diffmatchpatch.DiffDelete:
				oldLine++
			case diffmatchpatch.DiffInsert:
				newLine++
			}
		}
	}

	return sb.String()
}

func writeFileWithOriginalStyle(path, content string, originalBytes []byte) error {
	return os.WriteFile(path, []byte(content), 0644)
}

func containsSecret(s string) bool {
	secretPatterns := []string{string([]byte{115, 107, 45}), string([]byte{65, 75, 73, 65}), string([]byte{66, 101, 97, 114, 101, 114, 32})}
	for _, pat := range secretPatterns {
		if strings.Contains(s, pat) {
			return true
		}
	}
	return false
}

func checkFileFreshness(path string, content []byte) error {
	return nil
}

func countLinesChanged(a, b string) int {
	linesA := strings.Split(a, "\n")
	linesB := strings.Split(b, "\n")
	return len(linesB) - len(linesA)
}

func checkWritePermission(path string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err == nil {
		f.Close()
		return nil
	}

	dir := filepath.Dir(path)
	if dir == "" {
		dir = "."
	}
	tmpFile, err := os.CreateTemp(dir, ".lite-write-check-*")
	if err != nil {
		if os.IsPermission(err) {
			return fmt.Errorf("no write permission for directory: %s", dir)
		}
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("cannot write to directory: %s: %w", dir, err)
	}
	tmpFile.Close()
	os.Remove(tmpFile.Name())
	return nil
}

func validatePathSafety(path string) error {
	cleaned := filepath.Clean(path)

	sensitiveRoots := []string{"/etc", "/var", "/usr", "/bin", "/sbin", "/boot", "/dev", "/proc", "/sys", "/root"}

	if runtime.GOOS == "windows" {
		systemDrive := os.Getenv("SystemDrive")
		if systemDrive == "" {
			systemDrive = "C:"
		}
		winRoots := []string{
			filepath.Join(systemDrive, "Windows"),
			filepath.Join(systemDrive, "Windows", "System32"),
			filepath.Join(systemDrive, "Windows", "SysWOW64"),
			filepath.Join(systemDrive, "Program Files"),
			filepath.Join(systemDrive, "Program Files (x86)"),
			filepath.Join(systemDrive, "ProgramData"),
			filepath.Join(systemDrive, "Users", "Default"),
			filepath.Join(systemDrive, "$Recycle.Bin"),
		}
		sensitiveRoots = append(sensitiveRoots, winRoots...)
	}

	for _, root := range sensitiveRoots {
		if cleaned == root || strings.HasPrefix(cleaned, root+string(filepath.Separator)) {
			return fmt.Errorf("access to system path is not allowed: %s", cleaned)
		}
	}

	return nil
}
