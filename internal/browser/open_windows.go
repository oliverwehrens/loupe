//go:build windows

package browser

import "os/exec"

func startBrowser(url string) error {
	return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start() // #nosec G204 -- url is validated by ValidateURL (http/https only)
}
