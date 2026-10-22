//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// securePrivateDir replaces the directory DACL with a protected one that grants
// full control only to SYSTEM, Administrators and the current account, and
// stops inheritance from parent folders (for example a world-readable project
// directory). The machine directory of an installed service also grants the
// service's virtual account (NT SERVICE\NERVEAgent), so an administrator who
// re-enrolls does not lock the service out.
func securePrivateDir(dir string) error {
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil {
		return fmt.Errorf("read current account: %w", err)
	}
	sids := []string{user.User.Sid.String()}
	if sameDir(dir, machineStateDir()) {
		if sid, err := serviceAccountSID(); err == nil {
			sids = append(sids, sid)
		}
	}
	return restrictDir(dir, sids...)
}

// restrictDir sets a protected DACL: SYSTEM, Administrators and the given SIDs.
func restrictDir(dir string, sids ...string) error {
	sddl := "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"
	for _, sid := range sids {
		sddl += fmt.Sprintf("(A;OICI;FA;;;%s)", sid)
	}
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

// serviceAccountSID resolves the per-service SID. It exists only while the
// service is installed.
func serviceAccountSID() (string, error) {
	sid, _, _, err := windows.LookupSID("", serviceAccount)
	if err != nil {
		return "", err
	}
	return sid.String(), nil
}

func sameDir(a, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

// checkPrivateFile verifies the file exists. Access is controlled by the
// protected directory DACL applied at enrollment.
func checkPrivateFile(path string) error {
	_, err := os.Stat(path)
	return err
}

// checkPrivateConfig: access is controlled by the protected directory DACL.
func checkPrivateConfig(path string) error {
	_, err := os.Stat(path)
	return err
}

// preparePrivateFile: new files inherit the protected directory DACL.
func preparePrivateFile(*os.File, string) error { return nil }
