package plan

import (
	"fmt"
	"strings"
)

// Drone Docker images for running each toolchain. Remove the sha256 version when
// developing to make it easier to test out changes to a given toolchain. E.g.,
// `droneSrclibGoImage = "sourcegraph/srclib-go"`.
var (
	droneSrclibGoImage         = "sourcegraph/srclib-go@sha256:255d7742a47e500435cc2ad4e87e46a632c9b001aa09ead4c8400db53a6f185c"
	droneSrclibJavaScriptImage = "sourcegraph/srclib-javascript@sha256:6dd3fbad7b5cb4ae897d2b8ff88747321642028b15c77c152b8828971ec72601"
	droneSrclibJavaImage       = "sourcegraph/srclib-java@sha256:a9c411cd3b914504ba4d0b8c4ef023ee0794e8695089897e3e59b59ac54fc895"
	droneSrclibTypeScriptImage = "sourcegraph/srclib-typescript@sha256:a511770c9156236de59f5bb858039cd724527ba486b4e91297cd0a61fee20b4c"
	droneSrclibCSharpImage     = "sourcegraph/srclib-csharp@sha256:20850a4beeaf56e3b398b714294371b5e77f8139b903b0e07218e2964dad9afa"
	droneSrclibCSSImage        = "sourcegraph/srclib-css@sha256:7d619b5ceac0198b7f1911f2f535eda3e037b1489c52293090e6000093346987"
	droneSrclibPythonImage     = "sourcegraph/srclib-python@sha256:9ee27597169aaa7a77d5078e1e4f3d29642d04569d9afec4900d881b1e5e7651"
)

func versionHash(image string) (string, error) {
	split := strings.Split(image, "@sha256:")
	if len(split) != 2 {
		return "", fmt.Errorf("cannot parse version hash from toolchain image %s", image)
	}

	return split[1], nil
}

func SrclibVersion(lang string) (string, error) {
	switch lang {
	case "Go":
		return versionHash(droneSrclibGoImage)
	case "JavaScript":
		return versionHash(droneSrclibJavaScriptImage)
	case "Java":
		return versionHash(droneSrclibJavaImage)
	case "TypeScript":
		return versionHash(droneSrclibTypeScriptImage)
	case "C#":
		return versionHash(droneSrclibCSharpImage)
	case "CSS":
		return versionHash(droneSrclibCSSImage)
	}

	return "", fmt.Errorf("no srclib image found for %s", lang)
}
