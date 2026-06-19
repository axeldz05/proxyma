package main

import (
	"bytes"
	"os"
	"os/exec"
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

	// 2. Clear old app data/uninstall from the emulator
	t.Log("Uninstalling previous version of the app from emulator...")
	_ = exec.Command("adb", "uninstall", "com.proxyma.android").Run()

	// 3. Install the APK
	t.Log("Installing APK to emulator...")
	cmdInstall := exec.Command("adb", "install", "-r", "Proxyma_Node.apk")
	out, err := cmdInstall.CombinedOutput()
	require.NoError(t, err, "Failed to install APK: %s", string(out))

	// 4. Clear logcat to have fresh logs
	_ = exec.Command("adb", "logcat", "-c").Run()

	// 5. Start the app on the emulator
	t.Log("Starting the app on the emulator...")
	cmdStart := exec.Command("adb", "shell", "am", "start", "-n", "com.proxyma.android/org.fyne.fyne.app.MainActivity")
	out, err = cmdStart.CombinedOutput()
	require.NoError(t, err, "Failed to start app: %s", string(out))

	// 6. Wait for config load/creation (it sleeps 2 seconds in main.go)
	t.Log("Waiting for app to initialize and write config...")
	time.Sleep(5 * time.Second)

	// 7. Verify config.json is created under the application storage
	t.Log("Checking if config.json exists on the device...")
	// We check both /data/data and /data/user/0 paths as they map to the same internal storage
	cmdCheckFile1 := exec.Command("adb", "shell", "ls", "/data/data/com.proxyma.android/files/proxyma_data/config.json")
	fileOut1, errFile1 := cmdCheckFile1.CombinedOutput()

	cmdCheckFile2 := exec.Command("adb", "shell", "ls", "/data/user/0/com.proxyma.android/files/proxyma_data/config.json")
	fileOut2, errFile2 := cmdCheckFile2.CombinedOutput()

	// Collect logs for debugging if we fail
	cmdLogs := exec.Command("adb", "logcat", "-d")
	logOut, _ := cmdLogs.CombinedOutput()
	t.Logf("Logcat output during run:\n%s", string(logOut))

	// We require at least one of the paths to succeed in locating config.json
	if errFile1 != nil && errFile2 != nil {
		t.Fatalf("config.json was not created/loaded on the device!\nPath 1 Output: %s\nPath 2 Output: %s", string(fileOut1), string(fileOut2))
	}
	t.Log("Integration test passed: config.json exists and is readable.")
}
