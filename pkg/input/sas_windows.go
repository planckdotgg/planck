//go:build windows

package input

import "golang.org/x/sys/windows"

var procSendSAS = windows.NewLazySystemDLL("sas.dll").NewProc("SendSAS")

// SendSAS asks Windows for the secure attention sequence, as Ctrl+Alt+Del
// pressed on its own keyboard does; injected keys cannot. Windows acts on
// it only for a service, or an app it trusts with UI access, and only
// when the SoftwareSASGeneration policy allows that caller; otherwise it
// does nothing, and says nothing.
func SendSAS() error {
	if err := procSendSAS.Find(); err != nil {
		return err
	}
	procSendSAS.Call(1)
	return nil
}
