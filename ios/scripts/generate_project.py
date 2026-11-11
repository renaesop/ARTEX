#!/usr/bin/env python3
"""Generate a dependency-free Xcode project; no XcodeGen/CocoaPods required."""
from pathlib import Path
import hashlib

root = Path(__file__).resolve().parents[1]
project = root / 'ARTEX.xcodeproj'
project.mkdir(exist_ok=True)
objects = []
def uid(name): return hashlib.sha1(name.encode()).hexdigest()[:24].upper()
def add(name, body): objects.append(f'{uid(name)} = {{ {body} }};'); return uid(name)
def array(ids): return '(' + ','.join(ids) + ',)'

appfiles = sorted((root/'App').glob('*.swift')) + sorted((root/'Sources/ARTEXCore').glob('*.swift'))
tests = sorted((root/'UITests').glob('*.swift'))
refs = []; appbuild = []; testbuild = []
for p in appfiles + tests:
    path = p.relative_to(root).as_posix()
    refs.append(add(path, f'isa = PBXFileReference; lastKnownFileType = sourcecode.swift; path = "{path}"; sourceTree = "<group>";'))
    bid = add('build:'+path, f'isa = PBXBuildFile; fileRef = {uid(path)};')
    (testbuild if p in tests else appbuild).append(bid)
asset = add('assets', 'isa = PBXFileReference; lastKnownFileType = folder.assetcatalog; path = App/Assets.xcassets; sourceTree = "<group>";')
assetbuild = add('build:assets', f'isa = PBXBuildFile; fileRef = {asset};')
privacy = add('privacy', 'isa = PBXFileReference; lastKnownFileType = text.xml; path = App/PrivacyInfo.xcprivacy; sourceTree = "<group>";')
privacybuild = add('build:privacy', f'isa = PBXBuildFile; fileRef = {privacy};')
app = add('app-product', 'isa = PBXFileReference; explicitFileType = wrapper.application; path = ARTEX.app; sourceTree = BUILT_PRODUCTS_DIR;')
test = add('test-product', 'isa = PBXFileReference; explicitFileType = wrapper.cfbundle; path = ARTEXUITests.xctest; sourceTree = BUILT_PRODUCTS_DIR;')
products = add('products', f'isa = PBXGroup; children = {array([app,test])}; name = Products; sourceTree = "<group>";')
main = add('main', f'isa = PBXGroup; children = {array(refs+[asset,privacy,products])}; sourceTree = "<group>";')
base = 'SDKROOT = iphoneos; IPHONEOS_DEPLOYMENT_TARGET = 26.0; SWIFT_VERSION = 5.0; CLANG_ENABLE_MODULES = YES; ENABLE_USER_SCRIPT_SANDBOXING = YES;'
def configs(name, extra):
    ids=[]
    for config in ['Debug','Release']:
        debug = 'SWIFT_OPTIMIZATION_LEVEL = "-Onone"; SWIFT_ACTIVE_COMPILATION_CONDITIONS = "DEBUG $(inherited)"; DEBUG_INFORMATION_FORMAT = dwarf;' if config=='Debug' else 'SWIFT_COMPILATION_MODE = wholemodule; SWIFT_OPTIMIZATION_LEVEL = "-O"; DEBUG_INFORMATION_FORMAT = "dwarf-with-dsym";'
        ids.append(add(name+config, f'isa = XCBuildConfiguration; buildSettings = {{ {base} {debug} {extra} }}; name = {config};'))
    return add(name+'configs', f'isa = XCConfigurationList; buildConfigurations = {array(ids)}; defaultConfigurationIsVisible = 0; defaultConfigurationName = Release;')
projectconfigs=configs('project','')
shared='TARGETED_DEVICE_FAMILY = 1; CODE_SIGN_STYLE = Automatic; GENERATE_INFOPLIST_FILE = YES; CURRENT_PROJECT_VERSION = 1; MARKETING_VERSION = 0.1.0;'
appconfigs=configs('app', shared+' PRODUCT_BUNDLE_IDENTIFIER = com.youxiongs.artex; PRODUCT_NAME = ARTEX; ASSETCATALOG_COMPILER_APPICON_NAME = AppIcon; INFOPLIST_KEY_CFBundleDisplayName = ARTEX; INFOPLIST_KEY_LSApplicationCategoryType = "public.app-category.developer-tools"; INFOPLIST_KEY_UIApplicationSceneManifest_Generation = YES; INFOPLIST_KEY_UILaunchScreen_Generation = YES; INFOPLIST_KEY_UISupportedInterfaceOrientations = "UIInterfaceOrientationPortrait UIInterfaceOrientationLandscapeLeft UIInterfaceOrientationLandscapeRight"; INFOPLIST_KEY_NSLocalNetworkUsageDescription = "访问你通过 Tailscale 连接的 ARTEX 服务器。"; INFOPLIST_KEY_ITSAppUsesNonExemptEncryption = NO;')
testconfigs=configs('test', shared+' PRODUCT_BUNDLE_IDENTIFIER = com.youxiongs.artex.uitests; PRODUCT_NAME = ARTEXUITests; TEST_TARGET_NAME = ARTEX;')
appsource=add('app-sources', f'isa = PBXSourcesBuildPhase; buildActionMask = 2147483647; files = {array(appbuild)}; runOnlyForDeploymentPostprocessing = 0;')
testsource=add('test-sources', f'isa = PBXSourcesBuildPhase; buildActionMask = 2147483647; files = {array(testbuild)}; runOnlyForDeploymentPostprocessing = 0;')
resources=add('resources',f'isa = PBXResourcesBuildPhase; buildActionMask = 2147483647; files = {array([assetbuild,privacybuild])}; runOnlyForDeploymentPostprocessing = 0;')
frameworks=add('frameworks', 'isa = PBXFrameworksBuildPhase; buildActionMask = 2147483647; files = (); runOnlyForDeploymentPostprocessing = 0;')
apptarget=add('app-target', f'isa = PBXNativeTarget; buildConfigurationList = {appconfigs}; buildPhases = {array([appsource,frameworks,resources])}; buildRules = (); dependencies = (); name = ARTEX; productName = ARTEX; productReference = {app}; productType = "com.apple.product-type.application";')
proxy=add('proxy',f'isa = PBXContainerItemProxy; containerPortal = {uid("project")}; proxyType = 1; remoteGlobalIDString = {apptarget}; remoteInfo = ARTEX;')
dep=add('dependency',f'isa = PBXTargetDependency; target = {apptarget}; targetProxy = {proxy};')
testtarget=add('test-target',f'isa = PBXNativeTarget; buildConfigurationList = {testconfigs}; buildPhases = {array([testsource])}; buildRules = (); dependencies = {array([dep])}; name = ARTEXUITests; productName = ARTEXUITests; productReference = {test}; productType = "com.apple.product-type.bundle.ui-testing";')
add('project', f'isa = PBXProject; attributes = {{ LastUpgradeCheck = 2630; BuildIndependentTargetsInParallel = YES; TargetAttributes = {{ {apptarget} = {{ CreatedOnToolsVersion = 26.3; }}; {testtarget} = {{ CreatedOnToolsVersion = 26.3; TestTargetID = {apptarget}; }}; }}; }}; buildConfigurationList = {projectconfigs}; compatibilityVersion = "Xcode 14.0"; developmentRegion = zh-Hans; hasScannedForEncodings = 0; knownRegions = (en, Base, "zh-Hans"); mainGroup = {main}; productRefGroup = {products}; projectDirPath = ""; projectRoot = ""; targets = {array([apptarget,testtarget])};')
(project/'project.pbxproj').write_text('// !$*UTF8*$!\n{ archiveVersion = 1; classes = {}; objectVersion = 56; objects = {\n'+'\n'.join(objects)+f'\n}}; rootObject = {uid("project")}; }}\n')
scheme=project/'xcshareddata/xcschemes'; scheme.mkdir(parents=True,exist_ok=True)
def ref(target,name): return f'<BuildableReference BuildableIdentifier="primary" BlueprintIdentifier="{uid(target)}" BuildableName="{name}" BlueprintName="{name.split(".")[0]}" ReferencedContainer="container:ARTEX.xcodeproj"/>'
(scheme/'ARTEX.xcscheme').write_text(f'''<?xml version="1.0" encoding="UTF-8"?>
<Scheme LastUpgradeVersion="2630" version="1.3">
<BuildAction parallelizeBuildables="YES" buildImplicitDependencies="YES"><BuildActionEntries><BuildActionEntry buildForTesting="YES" buildForRunning="YES" buildForProfiling="YES" buildForArchiving="YES" buildForAnalyzing="YES">{ref('app-target','ARTEX.app')}</BuildActionEntry></BuildActionEntries></BuildAction>
<TestAction buildConfiguration="Debug" selectedDebuggerIdentifier="Xcode.DebuggerFoundation.Debugger.LLDB" selectedLauncherIdentifier="Xcode.IDEFoundation.Launcher.LLDB"><Testables><TestableReference skipped="NO">{ref('test-target','ARTEXUITests.xctest')}</TestableReference></Testables></TestAction>
<LaunchAction buildConfiguration="Debug" selectedDebuggerIdentifier="Xcode.DebuggerFoundation.Debugger.LLDB" selectedLauncherIdentifier="Xcode.IDEFoundation.Launcher.LLDB" launchStyle="0" useCustomWorkingDirectory="NO" ignoresPersistentStateOnLaunch="NO" debugDocumentVersioning="YES" debugServiceExtension="internal" allowLocationSimulation="YES"><BuildableProductRunnable runnableDebuggingMode="0">{ref('app-target','ARTEX.app')}</BuildableProductRunnable></LaunchAction>
<ProfileAction buildConfiguration="Release" shouldUseLaunchSchemeArgsEnv="YES" savedToolIdentifier="" useCustomWorkingDirectory="NO" debugDocumentVersioning="YES"><BuildableProductRunnable runnableDebuggingMode="0">{ref('app-target','ARTEX.app')}</BuildableProductRunnable></ProfileAction><AnalyzeAction buildConfiguration="Debug"/><ArchiveAction buildConfiguration="Release" revealArchiveInOrganizer="YES"/>
</Scheme>''')
print(project)
