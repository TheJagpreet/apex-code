package tools

import "net/http"

func NewDefaultRegistry() *Registry {
	gate := NewGate(DefaultGateOptions())
	reg := NewRegistry(gate)

	mustRegister(reg, NewReadFileTool(gate))
	mustRegister(reg, NewListDirTool(gate))
	mustRegister(reg, NewGlobTool(gate))
	mustRegister(reg, NewGrepTool(gate))
	mustRegister(reg, NewWriteFileTool(gate))
	mustRegister(reg, NewEditTool(gate))
	mustRegister(reg, NewRunTool(gate))
	mustRegister(reg, NewFetchWebTool(gate, http.DefaultClient))
	mustRegister(reg, NewFetchRawTool(gate, http.DefaultClient))
	mustRegister(reg, NewFetchJSONTool(gate, http.DefaultClient))
	mustRegister(reg, NewCloneRepoTool(gate))

	return reg
}

func mustRegister(reg *Registry, tool Tool) {
	if err := reg.Register(tool); err != nil {
		panic(err)
	}
}
