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

	serial := os.Getenv("ANDROID_SERIAL")
	if serial == "" {
		cmdDevs := exec.Command("adb", "devices")
		outDevs, errDevs := cmdDevs.Output()
		if errDevs == nil {
			lines := strings.Split(string(outDevs), "\n")
			for _, line := range lines {
				line = strings.TrimSpace(line)
				if line == "" || strings.HasPrefix(line, "List of") {
					continue
				}
				parts := strings.Fields(line)
				if len(parts) >= 2 && parts[1] == "device" {
					serial = parts[0]
					break
				}
			}
		}
		if serial == "" {
			serial = "emulator-5554"
		}
	}

	t.Logf("Target Android Device Serial: %s", serial)
	if !strings.HasPrefix(serial, "emulator-") {
		t.Log("⚠️ WARNING: Target device is a physical phone. If Google Play Protect blocks the install with 'Unsafe app blocked', please click 'Install anyway' on the phone screen to continue.")
	}

	adbCmd := func(args ...string) *exec.Cmd {
		allArgs := append([]string{"-s", serial}, args...)
		return exec.Command("adb", allArgs...)
	}

	// 2. Clear old app data/uninstall from the emulator
	t.Log("Uninstalling previous version of the app from emulator...")
	_ = adbCmd("uninstall", "com.proxyma.android").Run()

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
