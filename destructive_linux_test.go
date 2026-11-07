//go:build linux

package main

import "testing"

func TestDestructiveLinux(t *testing.T) {
	bad := []string{
		"rm -rf /tmp/x", "sudo rm file", "dd if=/dev/zero of=/dev/sda bs=1M", "mkfs.ext4 /dev/sdb1", "wipefs -a /dev/sdb",
		"parted /dev/nvme0n1 resizepart 3 100GB", "sgdisk -d 4 /dev/nvme0n1", "systemctl stop ssh", "systemctl --now disable foo",
		"apt remove -y firefox", "apt-get -y purge snapd", "dpkg -P pkg", "snap remove chromium", "ufw disable",
		"iptables -F", "reboot", "shutdown -h now", "userdel bob", "chmod -R 777 /var/www", "chown -R me: /opt",
		"mv foo /etc/bar", "find . -name '*.log' -delete", "echo x > /dev/sda", "echo 'x' | tee /etc/fstab",
		"sed -i 's/a/b/' /etc/fstab", "git reset --hard", "efibootmgr -b 0003 -B", "grub-install /dev/nvme0n1",
		"cryptsetup luksFormat /dev/sdb1", "fdisk /dev/sda", "sfdisk --delete /dev/sda 2", "truncate -s 0 big.log", "fsck.ext4 -y /dev/sda2", "resize2fs /dev/sda2 50G",
	}
	for _, c := range bad {
		if destructiveMatch(c) == "" {
			t.Errorf("not flagged: %s", c)
		}
	}
	ok := []string{
		"apt install -y ffmpeg", "apt update", "systemctl status ssh", "systemctl restart tokensaver-helper",
		"cat /etc/passwd", "ls -la /dev/sda", "git add .", "lsblk -f", "df -h", "ufw allow 8766/tcp",
		"cp a.txt b.txt", "dmesg | tail", "journalctl -u ssh -n 50", "mount /dev/sdb1 /mnt", "chmod +x run.sh",
		"echo hi > out.txt", "parted -l", "sudo fdisk -l", "fdisk -l /dev/sda", "fdisk-not-a-tool", "pkill -f llama-server",
	}
	for _, c := range ok {
		if m := destructiveMatch(c); m != "" {
			t.Errorf("wrongly flagged %q in: %s", m, c)
		}
	}
}
