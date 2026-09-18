//go:build !windows

package service

// runPlatform 在非 Windows 平台不特殊处理。
// macOS/Linux 的用户级与系统级统一交由 kardianos/service 处理
// （用户级通过 Options.UserService 选项）。
func runPlatform(prog Program, opt Options) (bool, error) {
	return false, nil
}

// controlPlatform 在非 Windows 平台不介入，交由 kardianos/service 处理。
func controlPlatform(action string, opt Options) (bool, error) {
	return false, nil
}

// statusPlatform 在非 Windows 平台不介入。
func statusPlatform(opt Options) (bool, string, error) {
	return false, "", nil
}
