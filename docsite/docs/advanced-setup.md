# Advanced Setup

## Uninstall

### User-local install (default)

This is the default when you ran `install.sh` without `OMNI_GLOBAL_INSTALL=1`:

```bash
# stop and disable the user service
systemctl --user disable --now omni.service

# remove service file, drop-ins, and enable symlink
rm -f ~/.config/systemd/user/omni.service
rm -rf ~/.config/systemd/user/omni.service.d/
rm -f ~/.config/systemd/user/default.target.wants/omni.service
systemctl --user daemon-reload

# remove symlinks
rm -f ~/.local/bin/omni
rm -f ~/.local/bin/omni-server

# remove install prefix
rm -rf ~/.local/opt/omni/

# optionally remove runtime state (DB, sessions)
rm -rf ~/.local/share/omni/
```

### System-wide install (`OMNI_GLOBAL_INSTALL=1`)

This applies when you ran `sudo install.sh` or `OMNI_GLOBAL_INSTALL=1 install.sh`:

```bash
# stop and remove the systemd service
sudo systemctl disable --now omni@$(whoami).service
sudo rm -f /etc/systemd/system/omni@.service
sudo systemctl daemon-reload

# remove symlinks
sudo rm -f /usr/local/bin/omni
sudo rm -f /usr/local/bin/omni-server

# remove install prefix
sudo rm -rf /opt/omni/
```
