package store

import provider "github.com/Shaik-Sirajuddin/omni/sandbox/provider"

type Info = provider.Info
type Sandbox = provider.Sandbox
type GetSandboxParams = provider.GetSandboxParams

type SandboxStore interface {
	Info() Info
	Create(*Sandbox) error
	Update(*Sandbox) error
	Get(*GetSandboxParams) (*Sandbox, error)
	List() ([]*Sandbox, error)
}
