#import <Cocoa/Cocoa.h>

static NSMenuItem *item(NSString *title, SEL action, NSString *key) {
  return [[NSMenuItem alloc] initWithTitle:title action:action keyEquivalent:key];
}

void tcWindowActivate(void) {
  [NSApp activateIgnoringOtherApps:YES];
  for (NSWindow *w in [NSApp windows]) {
    [w makeKeyAndOrderFront:nil];
  }
}

void tcWindowSetup(void *win) {
  NSWindow *w = (__bridge NSWindow *)win;
  // A regular app while the window is open: Dock icon, Cmd+Tab.
  [NSApp setActivationPolicy:NSApplicationActivationPolicyRegular];

  NSMenu *main = [NSMenu new];
  NSMenuItem *appItem = [NSMenuItem new];
  NSMenu *appMenu = [NSMenu new];
  [appMenu addItem:item(@"Hide TailConnect", @selector(hide:), @"h")];
  [appMenu addItem:[NSMenuItem separatorItem]];
  [appMenu addItem:item(@"Close Window", @selector(terminate:), @"q")];
  appItem.submenu = appMenu;
  [main addItem:appItem];

  // Without an Edit menu, Cmd+C/V/X/A/Z don't reach the web view.
  NSMenuItem *editItem = [NSMenuItem new];
  NSMenu *edit = [[NSMenu alloc] initWithTitle:@"Edit"];
  [edit addItem:item(@"Undo", @selector(undo:), @"z")];
  NSMenuItem *redo = item(@"Redo", @selector(redo:), @"z");
  redo.keyEquivalentModifierMask = NSEventModifierFlagCommand | NSEventModifierFlagShift;
  [edit addItem:redo];
  [edit addItem:[NSMenuItem separatorItem]];
  [edit addItem:item(@"Cut", @selector(cut:), @"x")];
  [edit addItem:item(@"Copy", @selector(copy:), @"c")];
  [edit addItem:item(@"Paste", @selector(paste:), @"v")];
  [edit addItem:item(@"Select All", @selector(selectAll:), @"a")];
  editItem.submenu = edit;
  [main addItem:editItem];

  NSMenuItem *viewItem = [NSMenuItem new];
  NSMenu *view = [[NSMenu alloc] initWithTitle:@"View"];
  [view addItem:item(@"Reload", @selector(reload:), @"r")];
  viewItem.submenu = view;
  [main addItem:viewItem];

  NSMenuItem *winItem = [NSMenuItem new];
  NSMenu *winMenu = [[NSMenu alloc] initWithTitle:@"Window"];
  [winMenu addItem:item(@"Minimize", @selector(performMiniaturize:), @"m")];
  [winMenu addItem:item(@"Zoom", @selector(performZoom:), @"")];
  [winMenu addItem:item(@"Close", @selector(performClose:), @"w")];
  winItem.submenu = winMenu;
  [main addItem:winItem];
  [NSApp setMainMenu:main];
  [NSApp setWindowsMenu:winMenu];

  w.minSize = NSMakeSize(720, 540); // keep in sync with MinWidth/MinHeight in window.go
  [w setFrameAutosaveName:@"TailConnectMain"];
  [w center];
  tcWindowActivate();
}
