package testprofile

import (
	"os"

	"golang.org/x/sys/windows"
)

// Chmod translates the private or publicly readable modes used by these fixtures.
func Chmod(path string, mode os.FileMode) error {
	user, err := windows.GetCurrentThreadEffectiveToken().GetTokenUser()
	if err != nil {
		return err
	}
	sddl := "D:P(A;OICI;FA;;;" + user.User.Sid.String() + ")(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"
	if mode.Perm()&0077 != 0 {
		sddl += "(A;OICI;GR;;;WD)"
	}
	return SetDACL(path, sddl)
}

func SetDACL(path, sddl string) error {
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return err
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}
