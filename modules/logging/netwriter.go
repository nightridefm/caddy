// Copyright 2015 Matthew Holt and The Caddy Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package logging

import (
	"fmt"
	"io"
	"net"

	"github.com/caddyserver/caddy/v2"
)

func init() {
	caddy.RegisterModule(NetWriter{})
}

// NetWriter implements a log writer that outputs to a network socket.
type NetWriter struct {
	Address string `json:"address,omitempty"`

	addr caddy.ParsedAddress
}

// CaddyModule returns the Caddy module information.
func (NetWriter) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "caddy.logging.writers.net",
		New: func() caddy.Module { return new(NetWriter) },
	}
}

// Provision sets up the module.
func (nw *NetWriter) Provision(ctx caddy.Context) error {
	repl := caddy.NewReplacer()
	address, err := repl.ReplaceOrErr(nw.Address, true, true)
	if err != nil {
		return fmt.Errorf("invalid host in address: %v", err)
	}

	nw.addr, err = caddy.ParseNetworkAddress(address)
	if err != nil {
		return fmt.Errorf("parsing network address '%s': %v", address, err)
	}

	if nw.addr.PortRangeSize() != 1 {
		return fmt.Errorf("multiple ports not supported")
	}

	return nil
}

func (nw NetWriter) String() string {
	return nw.addr.String()
}

// WriterKey returns a unique key representing this nw.
func (nw NetWriter) WriterKey() string {
	return nw.addr.String()
}

// OpenWriter opens a new network connection.
func (nw NetWriter) OpenWriter() (io.WriteCloser, error) {
	return net.Dial(nw.addr.Network, nw.addr.JoinHostPort(0))
}

// Interface guards
var (
	_ caddy.Provisioner  = (*NetWriter)(nil)
	_ caddy.WriterOpener = (*NetWriter)(nil)
)
