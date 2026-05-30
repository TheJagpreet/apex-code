package tools

// Plugin is the native extensibility seam for custom Go tool bundles. A plugin
// can register one or more tools against a registry during startup.
type Plugin interface {
	Name() string
	Register(registry *Registry) error
}

func RegisterPlugins(registry *Registry, plugins ...Plugin) error {
	for _, plugin := range plugins {
		if plugin == nil {
			continue
		}
		if err := plugin.Register(registry); err != nil {
			return err
		}
	}
	return nil
}
