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

package httpcaddyfile

import (
	"encoding/json"
	"sort"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

// directiveOrder specifies the order
// to apply directives in HTTP routes.
var directiveOrder = []string{
	"redir",
	"rewrite",

	"root",

	"strip_prefix",
	"strip_suffix",
	"uri_replace",
	"try_files",

	"basicauth",
	"header",
	"request_header",
	"encode",
	"templates",

	"handle",
	"route",

	"respond",
	"reverse_proxy",
	"php_fastcgi",
	"file_server",
}

// directiveIsOrdered returns true if dir is
// a known, ordered (sorted) directive.
func directiveIsOrdered(dir string) bool {
	for _, d := range directiveOrder {
		if d == dir {
			return true
		}
	}
	return false
}

// RegisterDirective registers a unique directive dir with an
// associated unmarshaling (setup) function. When directive dir
// is encountered in a Caddyfile, setupFunc will be called to
// unmarshal its tokens.
func RegisterDirective(dir string, setupFunc UnmarshalFunc) {
	if _, ok := registeredDirectives[dir]; ok {
		panic("directive " + dir + " already registered")
	}
	registeredDirectives[dir] = setupFunc
}

// RegisterHandlerDirective is like RegisterDirective, but for
// directives which specifically output only an HTTP handler.
// Directives registered with this function will always have
// an optional matcher token as the first argument.
func RegisterHandlerDirective(dir string, setupFunc UnmarshalHandlerFunc) {
	RegisterDirective(dir, func(h Helper) ([]ConfigValue, error) {
		if !h.Next() {
			return nil, h.ArgErr()
		}

		matcherSet, ok, err := h.MatcherToken()
		if err != nil {
			return nil, err
		}
		if ok {
			// strip matcher token; we don't need to
			// use the return value here because a
			// new dispenser should have been made
			// solely for this directive's tokens,
			// with no other uses of same slice
			h.Dispenser.Delete()
		}

		h.Dispenser.Reset() // pretend this lookahead never happened
		val, err := setupFunc(h)
		if err != nil {
			return nil, err
		}

		return h.NewRoute(matcherSet, val), nil
	})
}

// Helper is a type which helps setup a value from
// Caddyfile tokens.
type Helper struct {
	*caddyfile.Dispenser
	options      map[string]interface{}
	warnings     *[]caddyconfig.Warning
	matcherDefs  map[string]caddy.ModuleMap
	parentBlock  caddyfile.ServerBlock
	groupCounter counter
}

// Option gets the option keyed by name.
func (h Helper) Option(name string) interface{} {
	return h.options[name]
}

// Caddyfiles returns the list of config files from
// which tokens in the current server block were loaded.
func (h Helper) Caddyfiles() []string {
	// first obtain set of names of files involved
	// in this server block, without duplicates
	files := make(map[string]struct{})
	for _, segment := range h.parentBlock.Segments {
		for _, token := range segment {
			files[token.File] = struct{}{}
		}
	}
	// then convert the set into a slice
	filesSlice := make([]string, 0, len(files))
	for file := range files {
		filesSlice = append(filesSlice, file)
	}
	return filesSlice
}

// JSON converts val into JSON. Any errors are added to warnings.
func (h Helper) JSON(val interface{}) json.RawMessage {
	return caddyconfig.JSON(val, h.warnings)
}

// MatcherToken assumes the next argument token is (possibly) a matcher,
// and if so, returns the matcher set along with a true value. If the next
// token is not a matcher, nil and false is returned. Note that a true
// value may be returned with a nil matcher set if it is a catch-all.
func (h Helper) MatcherToken() (caddy.ModuleMap, bool, error) {
	if !h.NextArg() {
		return nil, false, nil
	}
	return matcherSetFromMatcherToken(h.Dispenser.Token(), h.matcherDefs, h.warnings)
}

// NewRoute returns config values relevant to creating a new HTTP route.
func (h Helper) NewRoute(matcherSet caddy.ModuleMap,
	handler caddyhttp.MiddlewareHandler) []ConfigValue {
	mod, err := caddy.GetModule(caddy.GetModuleID(handler))
	if err != nil {
		*h.warnings = append(*h.warnings, caddyconfig.Warning{
			File:    h.File(),
			Line:    h.Line(),
			Message: err.Error(),
		})
		return nil
	}
	var matcherSetsRaw []caddy.ModuleMap
	if matcherSet != nil {
		matcherSetsRaw = append(matcherSetsRaw, matcherSet)
	}
	return []ConfigValue{
		{
			Class: "route",
			Value: caddyhttp.Route{
				MatcherSetsRaw: matcherSetsRaw,
				HandlersRaw:    []json.RawMessage{caddyconfig.JSONModuleObject(handler, "handler", mod.ID.Name(), h.warnings)},
			},
		},
	}
}

// GroupRoutes adds the routes (caddyhttp.Route type) in vals to the
// same group, if there is more than one route in vals.
func (h Helper) GroupRoutes(vals []ConfigValue) {
	// ensure there's at least two routes; group of one is pointless
	var count int
	for _, v := range vals {
		if _, ok := v.Value.(caddyhttp.Route); ok {
			count++
			if count > 1 {
				break
			}
		}
	}
	if count < 2 {
		return
	}

	// now that we know the group will have some effect, do it
	groupName := h.groupCounter.nextGroup()
	for i := range vals {
		if route, ok := vals[i].Value.(caddyhttp.Route); ok {
			route.Group = groupName
			vals[i].Value = route
		}
	}
}

// NewBindAddresses returns config values relevant to adding
// listener bind addresses to the config.
func (h Helper) NewBindAddresses(addrs []string) []ConfigValue {
	return []ConfigValue{{Class: "bind", Value: addrs}}
}

// ConfigValue represents a value to be added to the final
// configuration, or a value to be consulted when building
// the final configuration.
type ConfigValue struct {
	// The kind of value this is. As the config is
	// being built, the adapter will look in the
	// "pile" for values belonging to a certain
	// class when it is setting up a certain part
	// of the config. The associated value will be
	// type-asserted and placed accordingly.
	Class string

	// The value to be used when building the config.
	// Generally its type is associated with the
	// name of the Class.
	Value interface{}

	directive string
}

func sortRoutes(routes []ConfigValue) {
	dirPositions := make(map[string]int)
	for i, dir := range directiveOrder {
		dirPositions[dir] = i
	}

	// while we are sorting, we will need to decode a route's path matcher
	// in order to sub-sort by path length; we can amortize this operation
	// for efficiency by storing the decoded matchers in a slice
	decodedMatchers := make([]caddyhttp.MatchPath, len(routes))

	sort.SliceStable(routes, func(i, j int) bool {
		iDir, jDir := routes[i].directive, routes[j].directive
		if iDir == jDir {
			// directives are the same; sub-sort by path matcher length
			// if there's only one matcher set and one path (common case)
			iRoute, ok := routes[i].Value.(caddyhttp.Route)
			if !ok {
				return false
			}
			jRoute, ok := routes[j].Value.(caddyhttp.Route)
			if !ok {
				return false
			}

			if len(iRoute.MatcherSetsRaw) == 1 && len(jRoute.MatcherSetsRaw) == 1 {
				// use already-decoded matcher, or decode if it's the first time seeing it
				iPM, jPM := decodedMatchers[i], decodedMatchers[j]
				if iPM == nil {
					var pathMatcher caddyhttp.MatchPath
					_ = json.Unmarshal(iRoute.MatcherSetsRaw[0]["path"], &pathMatcher)
					decodedMatchers[i] = pathMatcher
					iPM = pathMatcher
				}
				if jPM == nil {
					var pathMatcher caddyhttp.MatchPath
					_ = json.Unmarshal(jRoute.MatcherSetsRaw[0]["path"], &pathMatcher)
					decodedMatchers[j] = pathMatcher
					jPM = pathMatcher
				}

				// if there is only one path in the matcher, sort by
				// longer path (more specific) first
				if len(iPM) == 1 && len(jPM) == 1 {
					return len(iPM[0]) > len(jPM[0])
				}
			}
		}

		return dirPositions[iDir] < dirPositions[jDir]
	})
}

// serverBlock pairs a Caddyfile server block
// with a "pile" of config values, keyed by class
// name.
type serverBlock struct {
	block caddyfile.ServerBlock
	pile  map[string][]ConfigValue // config values obtained from directives
}

type (
	// UnmarshalFunc is a function which can unmarshal Caddyfile
	// tokens into zero or more config values using a Helper type.
	// These are passed in a call to RegisterDirective.
	UnmarshalFunc func(h Helper) ([]ConfigValue, error)

	// UnmarshalHandlerFunc is like UnmarshalFunc, except the
	// output of the unmarshaling is an HTTP handler. This
	// function does not need to deal with HTTP request matching
	// which is abstracted away. Since writing HTTP handlers
	// with Caddyfile support is very common, this is a more
	// convenient way to add a handler to the chain since a lot
	// of the details common to HTTP handlers are taken care of
	// for you. These are passed to a call to
	// RegisterHandlerDirective.
	UnmarshalHandlerFunc func(h Helper) (caddyhttp.MiddlewareHandler, error)
)

var registeredDirectives = make(map[string]UnmarshalFunc)
