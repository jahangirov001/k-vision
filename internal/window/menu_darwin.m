//go:build darwin && cgo

// The standard macOS menu bar for the K-Vision window. Without an Edit menu a
// WKWebView gets no Cmd+C / Cmd+V / Cmd+A at all (the shortcuts are menu items in
// AppKit), and without an app menu there is no Cmd+Q.
#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>

extern void kcAppearanceChanged(int dark);

static NSMenuItem *item(NSString *title, SEL action, NSString *key) {
	return [[NSMenuItem alloc] initWithTitle:title action:action keyEquivalent:key];
}

extern void kcMenuAction(char *action);

static NSString *kcVersion = @"";
static NSString *kcTagline = @"";

// Items that belong to the page (Settings, Command Palette, Back...) send their
// name to Go, which hands it to the page; see window.kcMenu in app.js.
@interface KCMenuTarget : NSObject
@end
@implementation KCMenuTarget
- (void)run:(NSMenuItem *)sender {
	kcMenuAction((char *)[(NSString *)sender.representedObject UTF8String]);
}
- (void)about:(id)sender {
	[NSApp activateIgnoringOtherApps:YES];
	NSMutableParagraphStyle *ps = [NSMutableParagraphStyle new];
	ps.alignment = NSTextAlignmentCenter;
	NSAttributedString *credits = [[NSAttributedString alloc] initWithString:kcTagline attributes:@{
		NSFontAttributeName: [NSFont systemFontOfSize:11], NSForegroundColorAttributeName: NSColor.secondaryLabelColor,
		NSParagraphStyleAttributeName: ps}];
	[NSApp orderFrontStandardAboutPanelWithOptions:@{
		NSAboutPanelOptionApplicationVersion: kcVersion,
		NSAboutPanelOptionVersion: @"", // no "(build)" after the version
		NSAboutPanelOptionCredits: credits,
	}];
}
// The web view in the key window, for zoom.
static WKWebView *findWebView(NSView *v) {
	if ([v isKindOfClass:[WKWebView class]]) return (WKWebView *)v;
	for (NSView *s in v.subviews) { WKWebView *w = findWebView(s); if (w) return w; }
	return nil;
}
// Restart: start a fresh copy of the app once this one has gone (so the new one gets
// the port and the window), then quit. A shell waits for this process to exit.
- (void)restartApp:(id)sender {
	NSString *app = NSBundle.mainBundle.bundlePath;
	int pid = NSProcessInfo.processInfo.processIdentifier;
	NSString *script = [NSString stringWithFormat:@"while kill -0 %d 2>/dev/null; do sleep 0.2; done; open -n \"$0\"", pid];
	NSTask *t = [NSTask new];
	t.launchPath = @"/bin/sh";
	t.arguments = @[@"-c", script, app];
	[t launch];
	[NSApp terminate:nil];
}
- (void)zoom:(NSMenuItem *)sender {
	NSWindow *win = NSApp.keyWindow ?: NSApp.mainWindow ?: NSApp.windows.firstObject;
	WKWebView *wv = findWebView(win.contentView);
	if (!wv) return;
	CGFloat z = wv.pageZoom;
	if (sender.tag == 0) z = 1.0;
	else if (sender.tag > 0) z = MIN(2.0, z + 0.1);
	else z = MAX(0.6, z - 0.1);
	wv.pageZoom = z;
}
@end
static KCMenuTarget *kcTarget;

static NSMenuItem *page(NSString *title, NSString *action, NSString *key, NSEventModifierFlags mods) {
	NSMenuItem *i = item(title, @selector(run:), key);
	i.target = kcTarget;
	i.representedObject = action;
	if (key.length) i.keyEquivalentModifierMask = mods;
	return i;
}
static NSMenu *menuNamed(NSMenu *bar, NSString *title) {
	NSMenuItem *top = [[NSMenuItem alloc] init];
	NSMenu *m = [[NSMenu alloc] initWithTitle:title];
	top.submenu = m;
	[bar addItem:top];
	return m;
}
#define CMD NSEventModifierFlagCommand
#define SHIFT NSEventModifierFlagShift
#define OPT NSEventModifierFlagOption
#define CTRL NSEventModifierFlagControl

void kcInstallMenu(const char *appName, const char *version, const char *tagline) {
	NSString *name = [NSString stringWithUTF8String:appName];
	kcVersion = [NSString stringWithUTF8String:version];
	kcTagline = [NSString stringWithUTF8String:tagline];
	kcTarget = [KCMenuTarget new];
	NSWindow.allowsAutomaticWindowTabbing = NO; // no Show Tab Bar / tab items: one window
	NSMenu *bar = [[NSMenu alloc] init];

	// ---- K-Vision
	NSMenu *app = menuNamed(bar, name);
	NSMenuItem *about = item([@"About " stringByAppendingString:name], @selector(about:), @"");
	about.target = kcTarget;
	[app addItem:about];
	[app addItem:[NSMenuItem separatorItem]];
	[app addItem:page(@"Settings…", @"settings", @",", CMD)];
	[app addItem:[NSMenuItem separatorItem]];
	NSMenuItem *services = item(@"Services", nil, @"");
	services.submenu = [[NSMenu alloc] initWithTitle:@"Services"];
	NSApp.servicesMenu = services.submenu;
	[app addItem:services];
	[app addItem:[NSMenuItem separatorItem]];
	[app addItem:item([@"Hide " stringByAppendingString:name], @selector(hide:), @"h")];
	NSMenuItem *others = item(@"Hide Others", @selector(hideOtherApplications:), @"h");
	others.keyEquivalentModifierMask = CMD | OPT;
	[app addItem:others];
	[app addItem:item(@"Show All", @selector(unhideAllApplications:), @"")];
	[app addItem:[NSMenuItem separatorItem]];
	NSMenuItem *restart = item([@"Restart " stringByAppendingString:name], @selector(restartApp:), @"r");
	restart.target = kcTarget;
	restart.keyEquivalentModifierMask = CMD | SHIFT;
	[app addItem:restart];
	[app addItem:item([@"Quit " stringByAppendingString:name], @selector(terminate:), @"q")];

	// ---- File
	NSMenu *file = menuNamed(bar, @"File");
	[file addItem:page(@"New Resource from YAML…", @"create", @"n", CMD)];
	[file addItem:[NSMenuItem separatorItem]];
	[file addItem:page(@"Start Screen", @"home", @"", 0)];
	[file addItem:page(@"Manage Clusters…", @"clusters", @"", 0)];
	[file addItem:page(@"Port Forwarding", @"forwards", @"", 0)];
	[file addItem:[NSMenuItem separatorItem]];
	[file addItem:item(@"Close Window", @selector(performClose:), @"w")];

	// ---- Edit
	NSMenu *edit = menuNamed(bar, @"Edit");
	[edit addItem:item(@"Undo", @selector(undo:), @"z")];
	NSMenuItem *redo = item(@"Redo", @selector(redo:), @"z");
	redo.keyEquivalentModifierMask = CMD | SHIFT;
	[edit addItem:redo];
	[edit addItem:[NSMenuItem separatorItem]];
	[edit addItem:item(@"Cut", @selector(cut:), @"x")];
	[edit addItem:item(@"Copy", @selector(copy:), @"c")];
	[edit addItem:item(@"Paste", @selector(paste:), @"v")];
	[edit addItem:item(@"Delete", @selector(delete:), @"")];
	[edit addItem:[NSMenuItem separatorItem]];
	[edit addItem:item(@"Select All", @selector(selectAll:), @"a")];
	[edit addItem:[NSMenuItem separatorItem]];
	[edit addItem:page(@"Find…", @"find", @"f", CMD)];
	// macOS adds AutoFill, Dictation and Emoji & Symbols to a menu titled "Edit".

	// ---- View
	NSMenu *view = menuNamed(bar, @"View");
	[view addItem:page(@"Command Palette…", @"palette", @"k", CMD)];
	[view addItem:[NSMenuItem separatorItem]];
	[view addItem:page(@"Namespace Overview", @"overview", @"", 0)];
	[view addItem:page(@"Cluster Topology", @"map-cluster", @"", 0)];
	[view addItem:page(@"Namespace Topology", @"map-namespace", @"", 0)];
	[view addItem:page(@"Helm Releases", @"helm", @"", 0)];
	[view addItem:page(@"All Clusters", @"everywhere", @"", 0)];
	[view addItem:[NSMenuItem separatorItem]];
	[view addItem:page(@"Back", @"back", @"[", CMD)];
	[view addItem:page(@"Forward", @"forward", @"]", CMD)];
	[view addItem:page(@"Refresh", @"refresh", @"r", CMD)];
	[view addItem:[NSMenuItem separatorItem]];
	NSMenuItem *z0 = item(@"Actual Size", @selector(zoom:), @"0"); z0.target = kcTarget; z0.tag = 0;
	NSMenuItem *z1 = item(@"Zoom In", @selector(zoom:), @"+"); z1.target = kcTarget; z1.tag = 1;
	NSMenuItem *z2 = item(@"Zoom Out", @selector(zoom:), @"-"); z2.target = kcTarget; z2.tag = -1;
	[view addItem:z0]; [view addItem:z1]; [view addItem:z2];
	[view addItem:[NSMenuItem separatorItem]];
	NSMenuItem *fs = item(@"Toggle Full Screen", @selector(toggleFullScreen:), @"f");
	fs.keyEquivalentModifierMask = CMD | CTRL;
	[view addItem:fs];

	// ---- Window
	NSMenu *win = menuNamed(bar, @"Window");
	[win addItem:item(@"Minimize", @selector(performMiniaturize:), @"m")];
	[win addItem:item(@"Zoom", @selector(performZoom:), @"")];
	[win addItem:[NSMenuItem separatorItem]];
	[win addItem:item(@"Bring All to Front", @selector(arrangeInFront:), @"")];
	NSApp.windowsMenu = win;

	// ---- Help (macOS puts the Search field at the top by itself)
	NSMenu *help = menuNamed(bar, @"Help");
	[help addItem:page(@"Keyboard Shortcuts", @"shortcuts", @"/", CMD)];
	[help addItem:page(@"Filter Syntax", @"filter-help", @"", 0)];
	[help addItem:page(@"Terminal Commands (kv)", @"cli-help", @"", 0)];
	[help addItem:[NSMenuItem separatorItem]];
	[help addItem:page(@"Show Audit Log", @"audit-log", @"", 0)];
	[help addItem:page(@"Show App Log", @"app-log", @"", 0)];
	NSApp.helpMenu = help;

	NSApp.mainMenu = bar;
}

// systemIsDark reads the system setting directly. AppleInterfaceStyle is "Dark"
// in dark mode and absent otherwise (Auto mode writes it as it switches).
// kcSystemIsDark asks AppKit, not the defaults database: the app's effective
// appearance is what Finder and Safari follow, and it covers the Auto setting
// (which flips with the time of day without writing AppleInterfaceStyle on every
// system) and any accessibility override.
// Before the app has finished launching, NSApp.effectiveAppearance is not settled yet
// (it came back dark on a light Mac), so the system default is the source of truth;
// AppleInterfaceStyle is "Dark" exactly when the menu bar is dark, Auto included,
// since macOS rewrites it as Auto switches. effectiveAppearance is only the change
// trigger, through the observer below.
int kcSystemIsDark(void) {
	CFPreferencesAppSynchronize(kCFPreferencesAnyApplication);
	CFStringRef v = (CFStringRef)CFPreferencesCopyAppValue(CFSTR("AppleInterfaceStyle"), kCFPreferencesAnyApplication);
	int dark = 0;
	if (v != NULL) {
		dark = CFGetTypeID(v) == CFStringGetTypeID() &&
			CFStringCompare(v, CFSTR("Dark"), kCFCompareCaseInsensitive) == kCFCompareEqualTo;
		CFRelease(v);
	}
	return dark;
}

// Watching NSApp.effectiveAppearance catches every switch, Auto included; the
// distributed notification below is kept as a second trigger.
@interface KCAppearanceObserver : NSObject
@end
@implementation KCAppearanceObserver
- (void)observeValueForKeyPath:(NSString *)keyPath ofObject:(id)object change:(NSDictionary *)change context:(void *)context {
	dispatch_async(dispatch_get_main_queue(), ^{ kcAppearanceChanged(kcSystemIsDark()); });
}
@end
static KCAppearanceObserver *kcObserver;


// kcWatchAppearance calls kcAppearanceChanged (in Go) whenever the system switches
// light/dark.
void kcWatchAppearance(void) {
	kcObserver = [KCAppearanceObserver new];
	[NSApp addObserver:kcObserver forKeyPath:@"effectiveAppearance" options:NSKeyValueObservingOptionNew context:NULL];
	[[NSDistributedNotificationCenter defaultCenter]
		addObserverForName:@"AppleInterfaceThemeChangedNotification" object:nil queue:[NSOperationQueue mainQueue]
		usingBlock:^(NSNotification *n) {
			// The default is written a moment after the notification on some systems.
			dispatch_after(dispatch_time(DISPATCH_TIME_NOW, (int64_t)(0.15 * NSEC_PER_SEC)), dispatch_get_main_queue(), ^{
				kcAppearanceChanged(kcSystemIsDark());
			});
		}];
}

// The window remembers its size and position between launches, like any Mac app.
void kcRememberFrame(void *window) {
	NSWindow *w = (__bridge NSWindow *)window;
	[w setFrameAutosaveName:@"K-VisionMain"];
	w.minSize = NSMakeSize(900, 560);
	// Follow the system appearance (nil = inherit), so the web view's
	// prefers-color-scheme matches it.
	w.appearance = nil;
	NSApp.appearance = nil;
}

// kcChooseFiles shows the standard macOS "Open" panel and returns the chosen paths,
// newline-separated (caller frees), or NULL when cancelled. It runs on the main
// thread -- AppKit requires it -- and waits there; the caller is a server goroutine.
char *kcChooseFiles(const char *title) {
	__block char *result = NULL;
	NSString *t = title ? [NSString stringWithUTF8String:title] : @"Choose files";
	void (^run)(void) = ^{
		[NSApp activateIgnoringOtherApps:YES];
		NSOpenPanel *p = [NSOpenPanel openPanel];
		p.canChooseFiles = YES;
		p.canChooseDirectories = NO;
		p.allowsMultipleSelection = YES;
		p.message = t;
		p.prompt = @"Upload";
		p.directoryURL = [NSURL fileURLWithPath:[NSHomeDirectory() stringByAppendingPathComponent:@"Downloads"]];
		if ([p runModal] == NSModalResponseOK) {
			NSMutableArray *paths = [NSMutableArray array];
			for (NSURL *u in p.URLs) [paths addObject:u.path];
			result = strdup([[paths componentsJoinedByString:@"\n"] UTF8String]);
		}
	};
	if ([NSThread isMainThread]) run(); else dispatch_sync(dispatch_get_main_queue(), run);
	return result;
}
