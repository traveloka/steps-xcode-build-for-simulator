package main

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/bitrise-io/go-utils/log"
	"github.com/bitrise-io/go-utils/pathutil"
	"github.com/bitrise-io/go-xcode/simulator"
	"github.com/bitrise-io/xcode-project/serialized"
	"github.com/bitrise-io/xcode-project/xcodeproj"
	"github.com/bitrise-io/xcode-project/xcscheme"
	"github.com/bitrise-io/xcode-project/xcworkspace"
	"github.com/bitrise-steplib/steps-xcode-build-for-simulator/util"
)

const (
	minSupportedXcodeMajorVersion = 7
	iOSSimName                    = "iphonesimulator"
	tvOSSimName                   = "appletvsimulator"
	watchOSSimName                = "watchsimulator"
)

const (
	bitriseXcodeRawResultTextEnvKey = "BITRISE_XCODE_RAW_RESULT_TEXT_PATH"
)

// Config ...
type Config struct {
	ProjectPath               string `env:"project_path,required"`
	Scheme                    string `env:"scheme,required"`
	Configuration             string `env:"configuration,required"`
	ArtifactName              string `env:"artifact_name"`
	XcodebuildOptions         string `env:"xcodebuild_options"`
	Workdir                   string `env:"workdir"`
	OutputDir                 string `env:"output_dir,required"`
	IsCleanBuild              bool   `env:"is_clean_build,opt[yes,no]"`
	OutputTool                string `env:"output_tool,opt[xcpretty,xcodebuild]"`
	SimulatorDevice           string `env:"simulator_device,required"`
	SimulatorOsVersion        string `env:"simulator_os_version,required"`
	SimulatorPlatform         string `env:"simulator_platform,opt[iOS,tvOS]"`
	DisableIndexWhileBuilding bool   `env:"disable_index_while_building,opt[yes,no]"`
	VerboseLog                bool   `env:"verbose_log,required"`
}

func main() {
	var scheme xcscheme.Scheme
	var schemeContainerDir string

	pth := "/Users/aronszabados/Downloads/PBX/Talabat.xcworkspace"
	schemeName := "Talabat"

	if xcodeproj.IsXcodeProj(pth) {
		project, err := xcodeproj.Open(pth)
		if err != nil {
			fmt.Println(err)
		}

		var ok bool
		scheme, ok = project.Scheme(schemeName)
		if !ok {
			fmt.Printf("no scheme found with name: %s in project: %s", schemeName, pth)
		}
		schemeContainerDir = filepath.Dir(pth)
		fmt.Println(scheme)
		fmt.Println(schemeContainerDir)
	} else if xcworkspace.IsWorkspace(pth) {
		workspace, err := xcworkspace.Open(pth)
		if err != nil {
			fmt.Println(err)
		}

		var containerProject string
		scheme, containerProject, err = workspace.Scheme(schemeName)
		if err != nil {
			fmt.Printf("no scheme found with name: %s in workspace: %s, error: %s", schemeName, pth, err)
		}
		schemeContainerDir = filepath.Dir(containerProject)
	} else {
		fmt.Println("unknown project extension: %s", filepath.Ext(pth))
	}
}

// findBuiltProject returns the Xcode project which will be built for the provided scheme
func findBuiltProject(pth, schemeName, configurationName string) (xcodeproj.XcodeProj, string, error) {
	var scheme xcscheme.Scheme
	var schemeContainerDir string

	if xcodeproj.IsXcodeProj(pth) {
		project, err := xcodeproj.Open(pth)
		if err != nil {
			return xcodeproj.XcodeProj{}, "", err
		}

		var ok bool
		scheme, ok = project.Scheme(schemeName)
		if !ok {
			return xcodeproj.XcodeProj{}, "", fmt.Errorf("no scheme found with name: %s in project: %s", schemeName, pth)
		}
		schemeContainerDir = filepath.Dir(pth)
	} else if xcworkspace.IsWorkspace(pth) {
		workspace, err := xcworkspace.Open(pth)
		if err != nil {
			return xcodeproj.XcodeProj{}, "", err
		}

		var containerProject string
		scheme, containerProject, err = workspace.Scheme(schemeName)
		if err != nil {
			return xcodeproj.XcodeProj{}, "", fmt.Errorf("no scheme found with name: %s in workspace: %s, error: %s", schemeName, pth, err)
		}
		schemeContainerDir = filepath.Dir(containerProject)
	} else {
		return xcodeproj.XcodeProj{}, "", fmt.Errorf("unknown project extension: %s", filepath.Ext(pth))
	}

	if configurationName == "" {
		configurationName = scheme.ArchiveAction.BuildConfiguration
	}

	if configurationName == "" {
		return xcodeproj.XcodeProj{}, "", fmt.Errorf("no configuration provided nor default defined for the scheme's (%s) archive action", schemeName)
	}

	var archiveEntry xcscheme.BuildActionEntry
	for _, entry := range scheme.BuildAction.BuildActionEntries {
		if entry.BuildForArchiving != "YES" || !entry.BuildableReference.IsAppReference() {
			continue
		}
		archiveEntry = entry
		break
	}

	if archiveEntry.BuildableReference.BlueprintIdentifier == "" {
		return xcodeproj.XcodeProj{}, "", fmt.Errorf("archivable entry not found")
	}

	projectPth, err := archiveEntry.BuildableReference.ReferencedContainerAbsPath(schemeContainerDir)
	if err != nil {
		return xcodeproj.XcodeProj{}, "", err
	}

	project, err := xcodeproj.Open(projectPth)
	if err != nil {
		return xcodeproj.XcodeProj{}, "", err
	}

	return project, scheme.Name, nil
}

// buildTargetDirForScheme returns the TARGET_BUILD_DIR for the provided scheme
func buildTargetDirForScheme(proj xcodeproj.XcodeProj, projectPath, scheme, configuration string, customOptions ...string) (string, error) {
	// Fetch project's main target from .xcodeproject
	var buildSettings serialized.Object
	if xcodeproj.IsXcodeProj(projectPath) {
		mainTarget, err := mainTargetOfScheme(proj, scheme)
		if err != nil {
			return "", fmt.Errorf("failed to fetch project's targets, error: %s", err)
		}

		buildSettings, err = proj.TargetBuildSettings(mainTarget.Name, configuration, customOptions...)
		if err != nil {
			return "", fmt.Errorf("failed to parse project (%s) build settings, error: %s", projectPath, err)
		}
	} else if xcworkspace.IsWorkspace(projectPath) {
		workspace, err := xcworkspace.Open(projectPath)
		if err != nil {
			return "", fmt.Errorf("Failed to open xcworkspace (%s), error: %s", projectPath, err)
		}

		buildSettings, err = workspace.SchemeBuildSettings(scheme, configuration, customOptions...)
		if err != nil {
			return "", fmt.Errorf("failed to parse workspace (%s) build settings, error: %s", projectPath, err)
		}
	} else {
		return "", fmt.Errorf("project file extension should be .xcodeproj or .xcworkspace, but got: %s", filepath.Ext(projectPath))

	}

	schemeBuildDir, err := buildSettings.String("TARGET_BUILD_DIR")
	if err != nil {
		return "", fmt.Errorf("failed to parse build settings, error: %s", err)
	}

	return schemeBuildDir, nil
}

// exportArtifacts exports the main target and it's .app dependencies.
func exportArtifacts(proj xcodeproj.XcodeProj, scheme string, schemeBuildDir string, configuration, simulatorPlatform, deployDir string, customOptions ...string) ([]string, error) {
	var exportedArtifacts []string
	splitSchemeDir := strings.Split(schemeBuildDir, "Build/")
	var schemeDir string

	// Split the scheme's TARGET_BUILD_DIR by the BUILD dir. This path will be the base path for the targets's TARGET_BUILD_DIR
	//
	// xcodebuild -showBuildSettings will produce different outputs if you call it with a -workspace & -scheme or if you call it with a -project & -target.
	// We need to call xcodebuild -showBuildSettings for all of the project targets to find the build artifacts (iOS, watchOS etc...)
	if len(splitSchemeDir) != 2 {
		log.Debugf("failed to parse scheme's build target dir: %s. Using the original build dir (%s)\n", schemeBuildDir, schemeBuildDir)
		schemeDir = schemeBuildDir
	} else {
		schemeDir = filepath.Join(splitSchemeDir[0], "Build")
	}

	mainTarget, err := mainTargetOfScheme(proj, scheme)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch project's targets, error: %s", err)
	}

	targets := append([]xcodeproj.Target{mainTarget}, mainTarget.DependentTargets()...)

	for _, target := range targets {
		log.Donef(target.Name + "...")

		// Is the target an application? -> If not skip the export
		if !strings.HasSuffix(target.ProductReference.Path, ".app") {
			log.Printf("Target (%s) is not an .app - SKIP", target.Name)
			continue
		}

		//
		// Find out the sdk for the target
		simulatorName := iOSSimName
		if simulatorPlatform == "tvOS" {
			simulatorName = tvOSSimName
		}
		{

			settings, err := proj.TargetBuildSettings(target.Name, configuration)
			if err != nil {
				log.Debugf("Failed to fetch project settings (%s), error: %s", proj.Path, err)
			}

			sdkRoot, err := settings.String("SDKROOT")
			if err != nil {
				log.Debugf("No SDKROOT config found for (%s) target", target.Name)
			}

			log.Debugf("sdkRoot: %s", sdkRoot)

			if strings.Contains(sdkRoot, "WatchOS.platform") {
				simulatorName = watchOSSimName
			}
		}

		//
		// Find the TARGET_BUILD_DIR for the target
		var targetDir string
		{
			customOptions = []string{"-sdk", simulatorName}
			buildSettings, err := proj.TargetBuildSettings(target.Name, configuration, customOptions...)
			if err != nil {
				return nil, fmt.Errorf("failed to get project build settings, error: %s", err)
			}

			buildDir, err := buildSettings.String("TARGET_BUILD_DIR")
			if err != nil {
				return nil, fmt.Errorf("failed to get build target dir for target (%s), error: %s", target.Name, err)
			}

			log.Debugf("Target (%s) TARGET_BUILD_DIR: %s", target.Name, buildDir)

			// Split the target's TARGET_BUILD_DIR by the BUILD dir. This path will be joined to the `schemeBuildDir`
			//
			// xcodebuild -showBuildSettings will produce different outputs if you call it with a -workspace & -scheme or if you call it with a -project & -target.
			// We need to call xcodebuild -showBuildSettings for all of the project targets to find the build artifacts (iOS, watchOS etc...)
			splitTargetDir := strings.Split(buildDir, "Build/")
			if len(splitTargetDir) != 2 {
				log.Debugf("failed to parse build target dir (%s) for target: %s. Using the original build dir (%s)\n", buildDir, target.Name, buildDir)
				targetDir = buildDir
			} else {
				targetDir = splitTargetDir[1]
			}

		}

		//
		// Copy - export
		{

			// Search for the generated build artifact in the next dirs:
			// Parent dir (main target's build dir by the provided scheme) + current target's build dir (This is a default for a nativ iOS project)
			// current target's build dir (If the project settings uses a custom TARGET_BUILD_DIR env)
			// .xcodeproj's directory + current target's build dir (If the project settings uses a custom TARGET_BUILD_DIR env & the project is not in the root dir)
			sourceDirs := []string{filepath.Join(schemeDir, targetDir), schemeDir, filepath.Join(path.Dir(proj.Path), schemeDir)}
			destination := filepath.Join(deployDir, target.ProductReference.Path)

			// Search for the generated build artifact
			var exported bool
			for _, sourceDir := range sourceDirs {
				source := filepath.Join(sourceDir, target.ProductReference.Path)
				log.Debugf("searching for the generated app in %s", source)

				if exists, err := pathutil.IsPathExists(source); err != nil {
					log.Debugf("failed to check if the path exists: (%s), error: ", source, err)
					continue

				} else if !exists {
					log.Debugf("path not exists: %s", source)
					continue
				}

				// Copy the build artifact
				cmd := util.CopyDir(source, destination)
				cmd.SetStdout(os.Stdout)
				cmd.SetStderr(os.Stderr)
				log.Debugf("$ " + cmd.PrintableCommandArgs())
				if err := cmd.Run(); err != nil {
					log.Debugf("failed to copy the generated app from (%s) to the Deploy dir\n", source)
					continue
				}

				exported = true
				break
			}

			if exported {
				exportedArtifacts = append(exportedArtifacts, destination)
				log.Debugf("Success\n")
			} else {
				return nil, fmt.Errorf("failed to copy the generated app to the Deploy dir")
			}
		}
	}

	return exportedArtifacts, nil
}

// mainTargetOfScheme return the main target
func mainTargetOfScheme(proj xcodeproj.XcodeProj, scheme string) (xcodeproj.Target, error) {
	projTargets := proj.Proj.Targets
	sch, ok := proj.Scheme(scheme)
	if !ok {
		return xcodeproj.Target{}, fmt.Errorf("Failed to found scheme (%s) in project", scheme)
	}

	var blueIdent string
	for _, entry := range sch.BuildAction.BuildActionEntries {
		if entry.BuildableReference.IsAppReference() {
			blueIdent = entry.BuildableReference.BlueprintIdentifier
			break
		}
	}

	// Search for the main target
	for _, t := range projTargets {
		if t.ID == blueIdent {
			return t, nil

		}
	}
	return xcodeproj.Target{}, fmt.Errorf("failed to find the project's main target for scheme (%s)", scheme)
}

// simulatorDestinationID return the simulator's ID for the selected device version.
func simulatorDestinationID(simulatorOsVersion, simulatorPlatform, simulatorDevice string) (string, error) {
	var simulatorID string

	if simulatorOsVersion == "latest" {
		info, _, err := simulator.GetLatestSimulatorInfoAndVersion(simulatorPlatform, simulatorDevice)
		if err != nil {
			return "", fmt.Errorf("failed to get latest simulator info - error: %s", err)
		}

		simulatorID = info.ID
		log.Printf("Latest simulator for %s = %s", simulatorDevice, simulatorID)
	} else {
		info, err := simulator.GetSimulatorInfo((simulatorPlatform + " " + simulatorOsVersion), simulatorDevice)
		if err != nil {
			return "", fmt.Errorf("failed to get simulator info (%s-%s) - error: %s", (simulatorPlatform + simulatorOsVersion), simulatorDevice, err)
		}

		simulatorID = info.ID
		log.Printf("Simulator for %s %s = %s", simulatorDevice, simulatorOsVersion, simulatorID)
	}
	return simulatorID, nil
}

func failf(format string, v ...interface{}) {
	log.Errorf(format, v...)
	os.Exit(1)
}
