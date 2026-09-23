package executor

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestEnsurePHPExifExtensionSkipsWhenFPMAlreadyLoadsExif(t *testing.T) {
	original := runImageOptimizerCommand
	t.Cleanup(func() { runImageOptimizerCommand = original })
	var calls []string
	runImageOptimizerCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, name)
		if name != "php-fpm8.3" {
			t.Fatalf("unexpected command %s %v", name, args)
		}
		return []byte("[PHP Modules]\nCore\nexif\njson\n"), nil
	}
	EnsurePHPExifExtension()
	if want := []string{"php-fpm8.3"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("commands = %v, want %v", calls, want)
	}
}

func TestEnsurePHPExifExtensionInstallsThenReloadsOnce(t *testing.T) {
	original := runImageOptimizerCommand
	t.Cleanup(func() { runImageOptimizerCommand = original })
	var calls []string
	probeCount := 0
	runImageOptimizerCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, name)
		switch name {
		case "php-fpm8.3":
			probeCount++
			if probeCount == 1 {
				return []byte("[PHP Modules]\nCore\njson\n"), nil
			}
			return []byte("[PHP Modules]\nCore\nexif\njson\n"), nil
		case "apt-get", "systemctl":
			return nil, nil
		default:
			t.Fatalf("unexpected command %s %v", name, args)
			return nil, nil
		}
	}
	EnsurePHPExifExtension()
	want := []string{"php-fpm8.3", "apt-get", "php-fpm8.3", "systemctl"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("commands = %v, want %v", calls, want)
	}
}

func TestEnsurePHPExifExtensionDoesNotReloadWhenInstallDoesNotLoadExif(t *testing.T) {
	original := runImageOptimizerCommand
	t.Cleanup(func() { runImageOptimizerCommand = original })
	var calls []string
	runImageOptimizerCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, name)
		switch name {
		case "php-fpm8.3":
			return []byte("[PHP Modules]\nCore\njson\n"), nil
		case "apt-get":
			return []byte("0 upgraded, 0 newly installed"), nil
		case "systemctl":
			t.Fatal("systemctl reload must not run when exif is still unavailable")
		}
		return nil, nil
	}
	EnsurePHPExifExtension()
	want := []string{"php-fpm8.3", "apt-get", "php-fpm8.3"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("commands = %v, want %v", calls, want)
	}
}

func TestEnsurePHPExifExtensionDoesNotReloadOnInstallFailure(t *testing.T) {
	original := runImageOptimizerCommand
	t.Cleanup(func() { runImageOptimizerCommand = original })
	var calls []string
	runImageOptimizerCommand = func(_ context.Context, name string, _ ...string) ([]byte, error) {
		calls = append(calls, name)
		if name == "apt-get" {
			return []byte("install failed"), errors.New("exit status 1")
		}
		return []byte("[PHP Modules]\nCore\n"), nil
	}
	EnsurePHPExifExtension()
	want := []string{"php-fpm8.3", "apt-get"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("commands = %v, want %v", calls, want)
	}
}
