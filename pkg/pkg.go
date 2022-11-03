package pkg

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"io/ioutil"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/gofrs/flock"
	gv "github.com/hashicorp/go-version"
	"github.com/octohelm/cuemod/pkg/cuemod/stdlib"
	"github.com/rs/zerolog/log"
	"go.dagger.io/dagger/version"
)

var (
	// FS contains the filesystem of the stdlib.
	//go:embed dagger.io universe.dagger.io
	FS embed.FS
)

func init() {
	stdlib.Register(FS, version.Version, DaggerModule, UniverseModule)
}

var (
	DaggerModule   = "dagger.io"
	UniverseModule = "universe.dagger.io"

	// ModuleRequirements specifies the MINIMUM version of the module dagger requires in order to work.
	// This must be updated whenever we make breaking changes so users are prompt to upgrade the packages.
	ModuleRequirements = map[string]*gv.Version{
		DaggerModule:   gv.Must(gv.NewVersion("0.2.11")),
		UniverseModule: gv.Must(gv.NewVersion("0.2.9")),
	}

	DaggerPackage     = fmt.Sprintf("%s/dagger", DaggerModule)
	DaggerCorePackage = fmt.Sprintf("%s/core", DaggerPackage)

	lockFilePath    = "dagger.lock"
	versionFilePath = path.Join("cue.mod", "version.txt")
)

func EnsureCompatibility(ctx context.Context, p string) error {
	// Skip version checking for development versions of dagger
	if version.Version == version.DevelopmentVersion {
		return nil
	}
	daggerVersion := gv.Must(gv.NewVersion(version.Version))

	if p == "" {
		p, _ = GetCueModParent()
	}
	cuePkgDir := path.Join(p, "cue.mod", "pkg")

	for module, minimumVersion := range ModuleRequirements {
		moduleDir := path.Join(cuePkgDir, module)

		// Skip version checking if the module is a symlink
		if fi, err := os.Lstat(moduleDir); err == nil {
			if fi.Mode()&os.ModeSymlink == os.ModeSymlink {
				continue
			}
		}

		versionFile := path.Join(moduleDir, versionFilePath)
		data, err := os.ReadFile(versionFile)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("failed to read %q: %w", versionFile, err)
			}

			return fmt.Errorf("package %q is incompatible with this version of dagger-cue (requires %s or newer). Run `dagger-cue project update` to resolve this", module, minimumVersion.String())
		}

		vendoredVersion, err := gv.NewVersion(strings.TrimSpace(string(data)))
		if err != nil {
			return fmt.Errorf("failed to parse %q: %w", versionFile, err)
		}

		if vendoredVersion.LessThan(minimumVersion) {
			return fmt.Errorf("package %q (version %s) is incompatible with this version of dagger-cue (requires %s or newer). Run `dagger-cue project update` to resolve this", module, vendoredVersion.String(), minimumVersion.String())
		}

		if vendoredVersion.GreaterThan(daggerVersion) {
			return fmt.Errorf("this plan requires dagger-cue %s or newer. Run `dagger-cue version --check` to check for latest version", vendoredVersion.String())
		}
	}

	return nil
}

func Vendor(ctx context.Context, p string) error {
	if p == "" {
		p, _ = GetCueModParent()
	}

	cuePkgDir := path.Join(p, "cue.mod", "pkg")
	if err := os.MkdirAll(cuePkgDir, 0755); err != nil {
		return err
	}

	// Lock this function so no more than 1 process can run it at once.
	lockFile := path.Join(cuePkgDir, lockFilePath)
	l := flock.New(lockFile)
	if err := l.Lock(); err != nil {
		return err
	}
	defer func() {
		l.Unlock()
		os.Remove(lockFile)
	}()

	// ensure cue module is initialized
	if err := CueModInit(ctx, p, ""); err != nil {
		return err
	}

	// remove 0.1-style .gitignore files
	gitignorePath := path.Join(cuePkgDir, ".gitignore")
	if contents, err := ioutil.ReadFile(gitignorePath); err == nil {
		if strings.HasPrefix(string(contents), "# generated by dagger") {
			os.Remove(gitignorePath)
		}
	}

	// generate `.gitattributes` file
	if err := os.WriteFile(
		path.Join(cuePkgDir, ".gitattributes"),
		[]byte("# generated by dagger\n** linguist-generated=true\n"),
		0600,
	); err != nil {
		return err
	}

	log.Ctx(ctx).Debug().Str("mod", p).Msg("vendoring packages")

	// Unpack modules in a temporary directory
	unpackDir, err := os.MkdirTemp(cuePkgDir, "vendor-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(unpackDir)

	if err := extractModules(unpackDir); err != nil {
		return err
	}

	for module := range ModuleRequirements {
		// Semi-atomic swap of the module
		//
		// The following basically does:
		// $ rm -rf cue.mod/pkg/MODULE.old
		// $ mv cue.mod/pkg/MODULE cue.mod/pkg/MODULE.old
		// $ mv VENDOR/MODULE cue.mod/pkg/MODULE
		// $ rm -rf cue.mod/pkg/MODULE.old

		newModuleDir := path.Join(unpackDir, module)
		moduleDir := path.Join(cuePkgDir, module)
		backupModuleDir := moduleDir + ".old"

		// Do not override the module if it's a symlink.
		if fi, err := os.Lstat(moduleDir); err == nil {
			if fi.Mode()&os.ModeSymlink == os.ModeSymlink {
				log.Ctx(ctx).Warn().Str("module", module).Msg("skip vendoring: module is symlinked")
				continue
			}
		}

		if version.Version != version.DevelopmentVersion {
			if err := os.WriteFile(path.Join(newModuleDir, versionFilePath), []byte(version.Version), 0600); err != nil {
				return err
			}
		}

		if err := os.RemoveAll(backupModuleDir); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.Rename(moduleDir, backupModuleDir); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		defer os.RemoveAll(backupModuleDir)

		if err := os.Rename(newModuleDir, moduleDir); err != nil {
			return err
		}
	}

	return nil
}

func extractModules(dest string) error {
	return fs.WalkDir(FS, ".", func(p string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if !entry.Type().IsRegular() {
			return nil
		}

		// Do not vendor the package's `cue.mod/pkg`
		if strings.Contains(p, "cue.mod/pkg") {
			return nil
		}

		contents, err := fs.ReadFile(FS, p)
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}

		overlayPath := path.Join(dest, p)

		if err := os.MkdirAll(filepath.Dir(overlayPath), 0755); err != nil {
			return err
		}

		// Give exec permission on embedded file to freely use shell script
		// Exclude permission linter
		//nolint
		return os.WriteFile(overlayPath, contents, 0700)
	})
}

// GetCueModParent traverses the directory tree up through ancestors looking for a cue.mod folder
func GetCueModParent(args ...string) (string, bool) {
	cwd, _ := os.Getwd()
	parentDir := cwd

	if len(args) == 1 {
		parentDir = args[0]
	}

	found := false

	for {
		if _, err := os.Stat(path.Join(parentDir, "cue.mod")); !errors.Is(err, os.ErrNotExist) {
			found = true
			break // found it!
		}

		parentDir = filepath.Dir(parentDir)

		if parentDir == fmt.Sprintf("%s%s", filepath.VolumeName(parentDir), string(os.PathSeparator)) {
			// reached the root
			parentDir = cwd // reset to working directory
			break
		}
	}

	return parentDir, found
}

func CueModInit(ctx context.Context, parentDir, module string) error {
	lg := log.Ctx(ctx)

	absParentDir, err := filepath.Abs(parentDir)
	if err != nil {
		return err
	}

	modDir := path.Join(absParentDir, "cue.mod")
	if err := os.MkdirAll(modDir, 0755); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return err
		}
	}

	modFile := path.Join(modDir, "module.cue")
	if _, err := os.Stat(modFile); err != nil {
		statErr, ok := err.(*os.PathError)
		if !ok {
			return statErr
		}

		lg.Debug().Str("mod", parentDir).Msg("initializing cue.mod")
		contents := fmt.Sprintf(`module: "%s"`, module)
		if err := os.WriteFile(modFile, []byte(contents), 0600); err != nil {
			return err
		}
	}

	if err := os.Mkdir(path.Join(modDir, "pkg"), 0755); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return err
		}
	}

	return nil
}
