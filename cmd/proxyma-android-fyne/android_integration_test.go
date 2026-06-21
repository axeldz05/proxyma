//go:build integration_android

package main

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAndroidConfigLoad(t *testing.T) {
	// 1. Build the APK using Fyne CLI
	t.Log("Building Fyne APK for Android...")
	cmdBuild := exec.Command("/home/drusila/go/bin/fyne", "package", "-os", "android", "-app-id", "com.proxyma.android", "-icon", "icon.png")
	cmdBuild.Dir = "."
	// Inject required environment variables for compilation
	cmdBuild.Env = append(cmdBuild.Environ(),
		"ANDROID_HOME=/opt/android-sdk",
		"ANDROID_NDK_HOME=/opt/android-ndk",
		"JAVA_HOME=/usr/lib/jvm/default",
		"PATH="+os.Getenv("PATH")+":/opt/android-sdk/build-tools/34.0.0:/opt/android-sdk/platform-tools:/usr/lib/jvm/default/bin:/home/drusila/go/bin",
	)
	var stdout, stderr bytes.Buffer
	cmdBuild.Stdout = &stdout
	cmdBuild.Stderr = &stderr
	err := cmdBuild.Run()
	if err != nil {
		t.Fatalf("Failed to build APK: %v\nStdout: %s\nStderr: %s", err, stdout.String(), stderr.String())
	}
	t.Log("APK built successfully.")

	var serial string
	var emulatorCmd *exec.Cmd

	useEmulatorEnv := os.Getenv("USE_EMULATOR") == "true"
	envSerial := os.Getenv("ANDROID_SERIAL")

	runningDevices, _ := getRunningDevices()

	if envSerial != "" {
		serial = envSerial
	} else if useEmulatorEnv {
		for _, d := range runningDevices {
			if strings.HasPrefix(d, "emulator-") {
				serial = d
				break
			}
		}
		if serial == "" {
			avds := getAvailableAVDs()
			if len(avds) == 0 {
				t.Fatal("USE_EMULATOR is true but no AVDs are configured/found.")
			}
			selectedAVD := avds[0]
			for _, avd := range avds {
				if avd == "proxyma_test_avd" {
					selectedAVD = avd
					break
				}
			}
			serial, emulatorCmd = startEmulator(t, selectedAVD)
			t.Cleanup(func() {
				if emulatorCmd != nil && emulatorCmd.Process != nil {
					t.Log("Cleaning up: killing started emulator...")
					_ = emulatorCmd.Process.Kill()
				}
			})
		}
	} else {
		for _, d := range runningDevices {
			if strings.HasPrefix(d, "emulator-") {
				serial = d
				break
			}
		}
		if serial == "" && len(runningDevices) > 0 {
			serial = runningDevices[0]
		}
		if serial == "" {
			avds := getAvailableAVDs()
			if len(avds) > 0 {
				t.Log("No active Android devices/emulators found. Auto-booting configured emulator AVD...")
				selectedAVD := avds[0]
				for _, avd := range avds {
					if avd == "proxyma_test_avd" {
						selectedAVD = avd
						break
					}
				}
				serial, emulatorCmd = startEmulator(t, selectedAVD)
				t.Cleanup(func() {
					if emulatorCmd != nil && emulatorCmd.Process != nil {
						t.Log("Cleaning up: killing started emulator...")
						_ = emulatorCmd.Process.Kill()
					}
				})
			} else {
				serial = "emulator-5554"
			}
		}
	}

	adbCmd := func(args ...string) *exec.Cmd {
		allArgs := append([]string{"-s", serial}, args...)
		return exec.Command("adb", allArgs...)
	}

	if strings.HasPrefix(serial, "emulator-") {
		t.Logf("🤖 Running test on EMULATED Android environment: %s", serial)
		// Try to disable package verification on emulator to avoid popups
		_ = adbCmd("shell", "settings", "put", "global", "package_verifier_enable", "0").Run()
		_ = adbCmd("shell", "settings", "put", "global", "package_verifier_user_consent", "-1").Run()
	} else {
		t.Logf("📱 Running test on PHYSICAL Android device: %s", serial)
		t.Log("⚠️ WARNING: Target device is a physical phone. If Google Play Protect blocks the install with 'Unsafe app blocked', please click 'Install anyway' on the phone screen to continue. Alternatively, you can run tests with USE_EMULATOR=true to automatically boot and test on a virtual emulator.")
	}

	// 2. Clear old app data/uninstall from the emulator (unless physical device or PRESERVE_DATA is true)
	preserveData := os.Getenv("PRESERVE_DATA") == "true" || !strings.HasPrefix(serial, "emulator-")
	if preserveData {
		t.Log("Preserving previous version and data (skipping uninstall)...")
	} else {
		t.Log("Uninstalling previous version of the app from emulator...")
		_ = adbCmd("uninstall", "com.proxyma.android").Run()
	}

	// 3. Install the APK
	t.Log("Installing APK to emulator...")
	cmdInstall := adbCmd("install", "-r", "Proxyma_Node.apk")
	out, err := cmdInstall.CombinedOutput()
	require.NoError(t, err, "Failed to install APK: %s", string(out))

	// 4. Clear logcat to have fresh logs
	_ = adbCmd("logcat", "-c").Run()

	// Dump logs automatically on failure
	t.Cleanup(func() {
		if t.Failed() {
			cmdLogs := adbCmd("logcat", "-d")
			logOut, _ := cmdLogs.CombinedOutput()
			t.Logf("Logcat output during failure:\n%s", string(logOut))
		}
	})

	// 5. Start the app on the emulator
	t.Log("Starting the app on the emulator...")
	cmdStart := adbCmd("shell", "am", "start", "-n", "com.proxyma.android/org.golang.app.GoNativeActivity")
	out, err = cmdStart.CombinedOutput()
	require.NoError(t, err, "Failed to start app: %s", string(out))

	// 6. Wait for config load/creation (it sleeps 2 seconds in main.go) using dynamic polling
	t.Log("Waiting for app to initialize and write config...")
	require.Eventually(t, func() bool {
		err1 := adbCmd("shell", "run-as", "com.proxyma.android", "ls", "files/proxyma_data/config.json").Run()
		return err1 == nil
	}, 6*time.Second, 200*time.Millisecond, "config.json was not created/loaded on the device!")

	t.Log("Integration test passed: config.json exists and is readable.")
}

func getRunningDevices() ([]string, error) {
	cmdDevs := exec.Command("adb", "devices")
	outDevs, errDevs := cmdDevs.Output()
	if errDevs != nil {
		return nil, errDevs
	}
	var serials []string
	lines := strings.Split(string(outDevs), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "List of") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) >= 2 && parts[1] == "device" {
			serials = append(serials, parts[0])
		}
	}
	return serials, nil
}

func getAvailableAVDs() []string {
	emulatorPath := "emulator"
	if _, err := exec.LookPath("emulator"); err != nil {
		emulatorPath = "/opt/android-sdk/tools/emulator"
	}
	cmd := exec.Command(emulatorPath, "-list-avds")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var avds []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			avds = append(avds, line)
		}
	}
	return avds
}

func startEmulator(t *testing.T, avdName string) (string, *exec.Cmd) {
	t.Logf("Starting Android emulator for AVD '%s'...", avdName)
	emulatorPath := "emulator"
	if _, err := exec.LookPath("emulator"); err != nil {
		emulatorPath = "/opt/android-sdk/tools/emulator"
	}
	cmd := exec.Command(emulatorPath, "-avd", avdName, "-no-audio", "-no-snapshot")
	if os.Getenv("ANDROID_EMULATOR_HEADLESS") == "true" {
		cmd.Args = append(cmd.Args, "-no-window")
	}

	err := cmd.Start()
	if err != nil {
		t.Fatalf("Failed to start emulator '%s': %v", avdName, err)
	}

	t.Log("Waiting for emulator to connect to adb...")
	var serial string
	for i := 0; i < 30; i++ {
		time.Sleep(2 * time.Second)
		devices, err := getRunningDevices()
		if err == nil {
			for _, d := range devices {
				if strings.HasPrefix(d, "emulator-") {
					serial = d
					break
				}
			}
		}
		if serial != "" {
			break
		}
	}
	if serial == "" {
		_ = cmd.Process.Kill()
		t.Fatalf("Emulator started but did not show up in adb within 60s")
	}

	t.Logf("Emulator detected with serial %s. Waiting for boot to complete...", serial)
	bootTimeout := 90 * time.Second
	bootChan := make(chan error, 1)
	go func() {
		for {
			cmdCheck := exec.Command("adb", "-s", serial, "shell", "getprop", "sys.boot_completed")
			outCheck, errCheck := cmdCheck.Output()
			if errCheck == nil && strings.TrimSpace(string(outCheck)) == "1" {
				bootChan <- nil
				return
			}
			time.Sleep(2 * time.Second)
		}
	}()

	select {
	case <-bootChan:
		t.Log("Emulator booted successfully.")
	case <-time.After(bootTimeout):
		_ = cmd.Process.Kill()
		t.Fatalf("Emulator boot timed out after %v", bootTimeout)
	}

	return serial, cmd
}
