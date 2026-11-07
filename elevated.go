package main

import (
	"regexp"
	"strings"
)

// The elevated helper runs as the user's own account with the admin token
// (a logon scheduled task with "highest privileges"). TokenSaver hands it
// commands marked elevated over a local named pipe that only this user's
// TokenSaver can open. Every command is written to an admin-only audit log.

const (
	elevPipeName = `\\.\pipe\tokensaver-elevated`
	elevTaskName = `TokenSaver\Elevated Helper`
)

// elevRequest is the one message the client sends.
type elevRequest struct {
	Command   string   `json:"command"`
	Shell     string   `json:"shell,omitempty"`
	Cwd       string   `json:"cwd,omitempty"`
	Env       []string `json:"env,omitempty"`
	Confirmed bool     `json:"confirmed,omitempty"`
}

// elevMsg is what the helper sends back, one JSON object per line:
// first {pid} (or {error}), then {out} chunks, then {exit}.
type elevMsg struct {
	PID   int    `json:"pid,omitempty"`
	Out   []byte `json:"out,omitempty"`
	Exit  *int   `json:"exit,omitempty"`
	Error string `json:"error,omitempty"`
}

// elevProc is a command running in the helper, seen from the client side.
type elevProc struct {
	pid  int
	done chan struct{}
	code *int
	err  error
	stop func()
}

// Commands that delete, wipe or change system state. The helper refuses these
// unless the request says the user confirmed them in chat.
var destructiveRe = func() *regexp.Regexp {
	b := `(?:^|[\s;&|(){}"'@])`
	e := `(?:\.exe)?(?:\s|$|[;&|)"'])`
	words := []string{
		`del`, `erase`, `rd`, `rmdir`, `rm`, `ri`, `format`, `diskpart`, `bcdedit`, `takeown`, `icacls`, `cacls`,
		`shutdown`, `bootrec`, `bcdboot`, `fsutil`, `mountvol`, `manage-bde`, `reagentc`,
		`Remove-Item`, `Clear-Content`, `Clear-Disk`, `Format-Volume`, `Remove-Partition`, `Initialize-Disk`,
		`Clear-RecycleBin`, `Remove-ItemProperty`, `Set-ItemProperty`, `New-ItemProperty`, `Clear-ItemProperty`,
		`Stop-Service`, `Remove-Service`, `Set-Service`, `New-Service`, `Set-Acl`,
		`Restart-Computer`, `Stop-Computer`, `Unregister-ScheduledTask`, `Disable-ScheduledTask`,
		`Uninstall-Package`, `Remove-AppxPackage`, `Set-MpPreference`, `Add-MpPreference`, `Remove-MpPreference`,
		`Disable-WindowsOptionalFeature`, `Set-ExecutionPolicy`, `Remove-LocalUser`, `Set-LocalUser`,
		`Remove-LocalGroupMember`, `Add-LocalGroupMember`, `Set-NetFirewallProfile`, `Remove-NetFirewallRule`,
		// Linux
		`unlink`, `shred`, `wipefs`, `dd`, `mkfs`, `mkswap`, `sgdisk`,
		`partprobe`, `resize2fs`, `ntfsresize`, `e2fsck`, `fsck`, `cryptsetup`, `lvremove`, `vgremove`, `pvremove`,
		`lvreduce`, `zpool`, `reboot`, `poweroff`, `halt`, `kexec`, `userdel`, `deluser`, `groupdel`, `delgroup`,
		`passwd`, `chpasswd`, `usermod`, `visudo`, `grub-install`, `update-grub`, `grub-mkconfig`, `efibootmgr`,
		`chattr`, `setfacl`, `truncate`, `blkdiscard`, `swapoff`,
	}
	phrases := []string{
		`reg(?:\.exe)?\s+(?:delete|add|import|restore|load|unload|copy)`,
		`sc(?:\.exe)?\s+(?:\\\\\S+\s+)?(?:delete|stop|config|create|failure|sdset)`,
		`net(?:\.exe)?\s+(?:stop|user|localgroup|share\s+\S+\s+/delete)`,
		`schtasks(?:\.exe)?\s+(?:/delete|/change)`,
		`vssadmin(?:\.exe)?\s+(?:delete|resize)`,
		`wbadmin(?:\.exe)?\s+delete`,
		`wmic(?:\.exe)?\s+.*\b(?:delete|call\s+terminate|call\s+uninstall)`,
		`cipher(?:\.exe)?\s+/w`,
		`winget(?:\.exe)?\s+(?:uninstall|remove)`,
		`msiexec(?:\.exe)?\s+.*/x`,
		`netsh(?:\.exe)?\s+(?:advfirewall|firewall)\s+.*\b(?:set|delete|reset)`,
		`dism(?:\.exe)?\s+.*/(?:remove|disable)`,
		`move(?:\.exe)?\s+.*(?:\\windows\\|\\program files)`,
		// Linux
		`mkfs\.\w+`, `fsck\.\w+`,
		`systemctl\s+(?:--\S+\s+)*(?:stop|disable|mask|kill|reboot|poweroff|halt|isolate|rescue|emergency|kexec|set-default)`,
		`service\s+\S+\s+stop`,
		`(?:apt|apt-get|aptitude)\s+(?:-\S+\s+)*(?:remove|purge|autoremove|full-upgrade|dist-upgrade)`,
		`dpkg\s+.*(?:-r|-P|--remove|--purge)`,
		`(?:snap|flatpak)\s+(?:remove|uninstall)`,
		`ufw\s+(?:disable|delete|reset|deny|reject)`,
		`(?:iptables|ip6tables)\s+.*(?:-F|-X|-D|--flush|--delete|-P\s+\S+\s+DROP)`,
		`nft\s+(?:flush|delete)`,
		`init\s+[06]`, `telinit\s+[06]`,
		`chmod\s+(?:-\S+\s+)*-R`, `chown\s+(?:-\S+\s+)*-R`, `chmod\s+-\S*R`, `chown\s+-\S*R`,
		`mv\s+.*\s/(?:etc|usr|boot|bin|sbin|lib|lib64|var)(?:/\S*)?`,
		`(?:fdisk|gdisk|cfdisk|sfdisk)\s+(?:-[^l\s]\S*\s+)*/dev/\S+`, `sfdisk\s+--(?:delete|part-type|relocate)`,
		`parted\s+.*\b(?:mklabel|mktable|mkpart|rm|resizepart|resize|set|toggle|name|rescue)`,
		`find\s+.*-delete`, `find\s+.*-exec\s+rm`,
		`(?:>|tee)\s*/dev/(?:sd|nvme|mmcblk|vd|hd)\w*`,
		`(?:>|tee)\s*/etc/(?:fstab|passwd|shadow|sudoers|group|crypttab)`,
		`sed\s+(?:-\S+\s+)*-i\S*\s+.*\s/(?:etc|boot)/\S*`,
		`(?:git)\s+(?:clean\s+-\S*f|reset\s+--hard)`,
	}
	for i, w := range words {
		words[i] = regexp.QuoteMeta(w)
	}
	return regexp.MustCompile(`(?i)` + b + `(?:` + strings.Join(words, "|") + `|` + strings.Join(phrases, "|") + `)` + e)
}()

// destructiveMatch returns the part of command that looks destructive, or "".
func destructiveMatch(command string) string {
	m := destructiveRe.FindString(command)
	return strings.Trim(m, " \t\r\n;&|(){}\"'@")
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ⏎ ")
	s = strings.ReplaceAll(s, "\n", " ⏎ ")
	return strings.ReplaceAll(s, "\r", " ")
}
