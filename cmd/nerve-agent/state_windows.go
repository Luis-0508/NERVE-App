//go:build windows

package main

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// securePrivateDir replaces the directory DACL with a protected one that grants
// full control only to SYSTEM, Administrators and the current account, and
// stops inheritance from parent folders (for example a world-readable project
// directory).
func securePrivateDir(dir string) error {
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil {
		return fmt.Errorf("read current account: %w", err)
	}
	sddl := fmt.Sprintf("D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FA;;;%s)", user.User.Sid.String())
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return fmt.Errorf("build security descriptor: %w", err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return fmt.Errorf("read security descriptor: %w", err)
	}
	return windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}

// checkPrivateFile verifies the file exists. Access is controlled by the
// protected directory DACL applied at enrollment.
func checkPrivateFile(path string) error {
	_, err := os.Stat(path)
	return err
}
