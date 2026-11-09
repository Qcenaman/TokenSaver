package main

// Android projects that came without a Gradle wrapper, and the project starter (local
// mode's new) for the full tool set: check action:new, then check action:build.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func gradleWrapperProps() string {
	return "distributionBase=GRADLE_USER_HOME\ndistributionPath=wrapper/dists\n" +
		"distributionUrl=https\\://services.gradle.org/distributions/gradle-" + gradleVersion + "-bin.zip\nzipStoreBase=GRADLE_USER_HOME\nzipStorePath=wrapper/dists\n"
}

// hasGradlew: the Gradle wrapper this system can run (gradlew.bat on Windows).
func hasGradlew(root string) bool {
	if isWin {
		return exists(filepath.Join(root, "gradlew.bat"))
	}
	return exists(filepath.Join(root, "gradlew"))
}

// ckAndroidPlugin: the Android Gradle plugin in the root or a module build file.
func ckAndroidPlugin(root string) bool {
	files, _ := filepath.Glob(filepath.Join(root, "*", "build.gradle*"))
	files = append(files, filepath.Join(root, "build.gradle"), filepath.Join(root, "build.gradle.kts"), filepath.Join(root, "gradle", "libs.versions.toml"))
	for _, f := range files {
		if ckFileHas(f, "com.android") {
			return true
		}
	}
	return false
}

// ckAndroidProject: an Android Gradle project, with or without the wrapper.
func ckAndroidProject(root string) bool {
	if ckAndroidPlugin(root) {
		return true
	}
	if !exists(filepath.Join(root, "settings.gradle")) && !exists(filepath.Join(root, "settings.gradle.kts")) {
		return false
	}
	m, _ := filepath.Glob(filepath.Join(root, "*", "src", "main", "AndroidManifest.xml"))
	return len(m) > 0
}

// addGradleWrapper gives an Android project the wrapper it lacks, so it builds from the command line.
func addGradleWrapper(root string) error {
	if err := checkAllowed(root); err != nil {
		return err
	}
	if err := fetchGradleWrapper(root); err != nil {
		return err
	}
	props := filepath.Join(root, "gradle", "wrapper", "gradle-wrapper.properties")
	if !exists(props) {
		if err := os.WriteFile(props, []byte(gradleWrapperProps()), 0o644); err != nil {
			return err
		}
	}
	if lp := filepath.Join(root, "local.properties"); !exists(lp) {
		if sdk := androidSDK(); sdk != "" {
			os.WriteFile(lp, []byte("sdk.dir="+filepath.ToSlash(sdk)+"\n"), 0o644)
		}
	}
	return nil
}

// androidEnv: the Gradle environment plus the Android SDK when ANDROID_HOME isn't set.
func androidEnv() []string {
	env := ckJavaEnv()
	if sdk := androidSDK(); sdk != "" && os.Getenv("ANDROID_HOME") == "" {
		env = append(env, "ANDROID_HOME="+sdk)
	}
	return env
}

// gradleOfflineMiss: an --offline build that failed because something isn't downloaded yet
// (a plugin or dependency Gradle couldn't resolve), not because of the code.
func gradleOfflineMiss(out string) bool {
	low := strings.ToLower(out)
	for _, s := range []string{"offline mode", "no cached version", "could not resolve", "could not find", "was not found in any of the following sources"} {
		if strings.Contains(low, s) {
			return true
		}
	}
	return false
}

func dropArg(args []string, drop string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		if a != drop {
			out = append(out, a)
		}
	}
	return out
}

// toolCheckNew is check action:new: local mode's project starter for the full tool set.
// A GitHub repo is only made when asked for (repo:true).
func toolCheckNew(raw json.RawMessage) (string, error) {
	var a map[string]any
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("bad arguments: %v", err)
	}
	if a["repo"] != true {
		a["repo"] = false
	}
	out, err := toolNew(a)
	if err != nil {
		return "", err
	}
	return out + "\nBuild it with check action:build and path = that folder (Android: makes the debug APK).", nil
}
