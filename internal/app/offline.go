package app

// EnableOffline is startup configuration for a single invocation. Offline
// installation and repair retain the compiled catalog approval boundary.
func (core *Core) EnableOffline() {
	core.installer.Client.Offline = true
	if core.syncer != nil && core.syncer.Client != nil {
		core.syncer.Client.Offline = true
	}
	if core.upgrader != nil && core.upgrader.Client != nil {
		core.upgrader.Client.Offline = true
	}
}

func (core *Core) offline() bool {
	return core.installer != nil && core.installer.Client != nil && core.installer.Client.Offline
}
