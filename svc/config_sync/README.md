# Config Sync Service

Draft implementation.

This package contains the initial `config_sync` service for synchronizing omni hook configuration into active agent hook transformers. It is currently scoped to the service and registry implementation only; runtime wiring through operator, CLI, server startup, and the separate hook operator service is still pending.

Source config is read through `config.OmniConfigResolver`, which defaults to the XDG user config path for `omni/config.json`.

## Agy Settings Sync

Draft support exists for agy settings synchronization through `SettingsSyncTarget`.

- Default settings are read through `codeagent.SettingsResolver`.
- Agy defaults are mirrored into workspace `.agy/settings.json`.
- Workspace `.agy/settings.json` is watched with a polling watcher and can sync supported fields back through `SettingsResolver.SaveDefaultSettings`.
- Supported fields are the shared `codeagent.Settings` fields currently mapped by the agy connector: model, permission mode, and sandbox.

Runtime registration with omni-server still requires wiring from the operator/server side.
