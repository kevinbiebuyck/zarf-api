// Package zarfz adapts zarf's own package-level entrypoints (the exact
// functions the zarf CLI commands call) to plain function calls the HTTP
// layer can use. Keeping this layer a thin mirror of src/cmd/package.go is
// deliberate: the CLI is zarf's most stable contract, so upgrades of the
// vendored submodule should require little to no changes here.
package zarfz

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/zarf-dev/zarf/src/config"
	"github.com/zarf-dev/zarf/src/pkg/cluster"
	"github.com/zarf-dev/zarf/src/pkg/packager"
	"github.com/zarf-dev/zarf/src/pkg/packager/filters"
	"github.com/zarf-dev/zarf/src/pkg/packager/layout"
	"github.com/zarf-dev/zarf/src/pkg/state"
	"github.com/zarf-dev/zarf/src/pkg/value"
)

// Duration is a time.Duration that unmarshals from either a JSON string
// ("15m") or a JSON number (nanoseconds).
type Duration time.Duration

// UnmarshalJSON implements json.Unmarshaler.
func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		parsed, err := time.ParseDuration(s)
		if err != nil {
			return fmt.Errorf("invalid duration %q: %w", s, err)
		}
		*d = Duration(parsed)
		return nil
	}
	var n int64
	if err := json.Unmarshal(b, &n); err != nil {
		return fmt.Errorf("duration must be a string (\"15m\") or nanoseconds: %w", err)
	}
	*d = Duration(n)
	return nil
}

// MarshalJSON implements json.Marshaler.
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// DeployRequest mirrors the flag set of `zarf package deploy`.
type DeployRequest struct {
	// Source optionally overrides the package source. Empty means "use the
	// stored package identified in the URL". Anything zarf understands works
	// here: local tarball path, oci:// reference, https:// URL.
	Source string `json:"source,omitempty"`
	// Components is a comma-separated list of optional components to deploy.
	Components string `json:"components,omitempty"`
	// SetVariables maps to --set-variables (###ZARF_VAR_*### templating).
	SetVariables map[string]string `json:"setVariables,omitempty"`
	// SetValues maps to --set-values (dot-path helm values, typed by inference).
	SetValues map[string]string `json:"setValues,omitempty"`
	// Values are inline helm values, merged before SetValues.
	Values map[string]any `json:"values,omitempty"`

	NamespaceOverride string `json:"namespaceOverride,omitempty"`
	TakeOwnership     bool   `json:"takeOwnership,omitempty"`
	Connected         bool   `json:"connected,omitempty"`
	ForceConflicts    bool   `json:"forceConflicts,omitempty"`

	// Timeout for helm operations (e.g. "15m"). Zero uses zarf's default.
	Timeout Duration `json:"timeout,omitempty"`
	// Retries for retryable deploy operations. Zero uses zarf's default.
	Retries        int `json:"retries,omitempty"`
	OCIConcurrency int `json:"ociConcurrency,omitempty"`

	// Shasum optionally verifies the package tarball checksum before deploy.
	Shasum string `json:"shasum,omitempty"`
	// Verify is one of "never", "if-possible" (default), "always".
	Verify string `json:"verify,omitempty"`
	// PublicKeyPath optionally points to a cosign public key for verification.
	PublicKeyPath string `json:"publicKeyPath,omitempty"`

	SkipValuesSchemaValidation bool `json:"skipValuesSchemaValidation,omitempty"`
	SkipVersionCheck           bool `json:"skipVersionCheck,omitempty"`
}

// RemoveRequest mirrors the flag set of `zarf package remove`.
type RemoveRequest struct {
	// Components limits removal to a comma-separated list of components.
	Components       string `json:"components,omitempty"`
	NamespaceOverride string `json:"namespaceOverride,omitempty"`
	// Timeout for helm operations. Zero uses zarf's default.
	Timeout Duration `json:"timeout,omitempty"`
	// SetValues maps to --set-values at remove time.
	SetValues map[string]string `json:"setValues,omitempty"`
	// Values are inline helm values, merged before SetValues.
	Values           map[string]any `json:"values,omitempty"`
	SkipVersionCheck bool           `json:"skipVersionCheck,omitempty"`
}

// DeployedPackageInfo mirrors the output of `zarf package list`.
type DeployedPackageInfo struct {
	Package           string                    `json:"package"`
	NamespaceOverride string                    `json:"namespaceOverride,omitempty"`
	Version           string                    `json:"version"`
	Connectivity      state.PackageConnectivity `json:"connectivity"`
	Components        []DeployedComponentInfo   `json:"components"`
	Generation        int                       `json:"generation"`
	Digest            string                    `json:"digest,omitempty"`
	CLIVersion        string                    `json:"cliVersion,omitempty"`
}

// DeployedComponentInfo is one deployed component's name and status.
type DeployedComponentInfo struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

// Deploy mirrors `zarf package deploy --confirm` (non-interactive). The
// package source is loaded, then handed to packager.Deploy — the same
// sequence the CLI performs in src/cmd/package.go.
func Deploy(ctx context.Context, req DeployRequest) (*packager.DeployResult, error) {
	if req.Source == "" {
		return nil, fmt.Errorf("a package source is required")
	}

	// Non-interactive deploy: only pull the layers we actually need, exactly
	// like the CLI does once --confirm is passed.
	filter := filters.Combine(
		filters.ByLocalOS(runtime.GOOS),
		filters.ForDeploy(req.Components, false),
	)

	cachePath, err := config.GetAbsCachePath()
	if err != nil {
		return nil, err
	}

	strategy, err := verifyStrategy(req.Verify)
	if err != nil {
		return nil, err
	}

	loadOpt := packager.LoadOptions{
		Shasum:               req.Shasum,
		VerificationStrategy: strategy,
		PublicKeyPath:        req.PublicKeyPath,
		Filter:               filter,
		Architecture:         config.GetArch(),
		OCIConcurrency:       req.OCIConcurrency,
		CachePath:            cachePath,
	}
	pkgLayout, err := packager.LoadPackage(ctx, req.Source, loadOpt)
	if err != nil {
		return nil, fmt.Errorf("unable to load package: %w", err)
	}
	defer func() { _ = pkgLayout.Cleanup() }()

	values, err := buildValues(req.Values, req.SetValues)
	if err != nil {
		return nil, err
	}

	deployOpts := packager.DeployOptions{
		Values:                     values,
		TakeOwnership:              req.TakeOwnership,
		Connected:                  req.Connected,
		ForceConflicts:             req.ForceConflicts,
		Timeout:                    time.Duration(req.Timeout),
		Retries:                    req.Retries,
		OCIConcurrency:             req.OCIConcurrency,
		SetVariables:               req.SetVariables,
		NamespaceOverride:          req.NamespaceOverride,
		IsInteractive:              false,
		SkipValuesSchemaValidation: req.SkipValuesSchemaValidation,
		SkipVersionCheck:           req.SkipVersionCheck,
	}

	result, err := packager.Deploy(ctx, pkgLayout, deployOpts)
	if err != nil {
		return nil, fmt.Errorf("failed to deploy package: %w", err)
	}
	return &result, nil
}

// Remove mirrors `zarf package remove --confirm` (non-interactive): the
// package definition is read back from the cluster's deployed-package secret
// and handed to packager.Remove.
func Remove(ctx context.Context, name string, req RemoveRequest) error {
	// A cluster connection may or may not be required depending on the
	// package; the CLI ignores the error for the same reason.
	c, _ := cluster.New(ctx) //nolint:errcheck

	filter := filters.Combine(
		filters.ByLocalOS(runtime.GOOS),
		filters.BySelectState(req.Components),
	)
	cachePath, err := config.GetAbsCachePath()
	if err != nil {
		return err
	}
	loadOpts := packager.LoadOptions{
		VerificationStrategy: layout.VerifyIfPossible,
		Architecture:         config.GetArch(),
		Filter:               filter,
		CachePath:            cachePath,
	}
	pkg, err := packager.GetPackageFromSourceOrCluster(ctx, c, name, req.NamespaceOverride, loadOpts)
	if err != nil {
		return fmt.Errorf("unable to load the package: %w", err)
	}

	values, err := buildValues(req.Values, req.SetValues)
	if err != nil {
		return err
	}

	return packager.Remove(ctx, pkg, packager.RemoveOptions{
		Cluster:           c,
		Timeout:           time.Duration(req.Timeout),
		NamespaceOverride: req.NamespaceOverride,
		SkipVersionCheck:  req.SkipVersionCheck,
		Values:            values,
	})
}

// ListDeployed mirrors `zarf package list`: it reads the deployed-package
// secrets from the cluster's zarf namespace.
func ListDeployed(ctx context.Context) ([]DeployedPackageInfo, error) {
	c, err := cluster.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to connect to the Kubernetes cluster: %w", err)
	}
	deployed, err := c.GetDeployedZarfPackages(ctx)
	if err != nil && len(deployed) == 0 {
		return nil, fmt.Errorf("unable to get the packages deployed to the cluster: %w", err)
	}

	out := make([]DeployedPackageInfo, 0, len(deployed))
	for _, pkg := range deployed {
		info := DeployedPackageInfo{
			Package:           pkg.Name,
			NamespaceOverride: pkg.NamespaceOverride,
			Version:           pkg.Data.Metadata.Version,
			Connectivity:      pkg.GetPackageConnectivity(),
			Generation:        pkg.Generation,
			Digest:            pkg.Digest,
			CLIVersion:        pkg.CLIVersion,
			Components:        make([]DeployedComponentInfo, 0, len(pkg.DeployedComponents)),
		}
		for _, comp := range pkg.DeployedComponents {
			info.Components = append(info.Components, DeployedComponentInfo{
				Name:   comp.Name,
				Status: string(comp.Status),
			})
		}
		out = append(out, info)
	}
	return out, nil
}

// GetDeployed returns the full recorded state of one deployed package,
// including the original package definition.
func GetDeployed(ctx context.Context, name, namespaceOverride string) (*state.DeployedPackage, error) {
	c, err := cluster.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to connect to the Kubernetes cluster: %w", err)
	}
	return c.GetDeployedPackage(ctx, name, state.WithPackageNamespaceOverride(namespaceOverride))
}

// Ping checks whether a Kubernetes cluster is reachable.
func Ping(ctx context.Context) error {
	_, err := cluster.New(ctx)
	return err
}

// buildValues merges inline values with dot-path set-values, mirroring the
// CLI's parseValues (src/cmd/dev.go).
func buildValues(values map[string]any, setValues map[string]string) (value.Values, error) {
	out := value.Values{}
	for k, v := range values {
		out[k] = v
	}
	for key, val := range setValues {
		path := value.Path(key)
		if !strings.HasPrefix(key, ".") {
			path = value.Path("." + key)
		}
		if err := out.Set(path, value.InferType(val)); err != nil {
			return nil, fmt.Errorf("unable to set value at path %s: %w", key, err)
		}
	}
	return out, nil
}

func verifyStrategy(mode string) (layout.VerificationStrategy, error) {
	switch mode {
	case "", "if-possible":
		return layout.VerifyIfPossible, nil
	case "never":
		return layout.VerifyNever, nil
	case "always":
		return layout.VerifyAlways, nil
	default:
		return layout.VerifyIfPossible, fmt.Errorf("invalid verify value %q (must be never, if-possible, or always)", mode)
	}
}
