#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>

@interface FFTaggerDelegate : NSObject <NSApplicationDelegate, NSWindowDelegate, WKNavigationDelegate, WKUIDelegate>
@property(nonatomic, retain) NSWindow *window;
@property(nonatomic, retain) WKWebView *web;
@property(nonatomic, retain) NSString *host;
@property(nonatomic, retain) NSNumber *port;
@end

@implementation FFTaggerDelegate
- (void)webView:(WKWebView *)webView didFailProvisionalNavigation:(WKNavigation *)navigation withError:(NSError *)error { NSLog(@"FFTagger navigation: %@",error); }
- (void)webView:(WKWebView *)webView didFailNavigation:(WKNavigation *)navigation withError:(NSError *)error { NSLog(@"FFTagger navigation: %@",error); }
- (void)webViewWebContentProcessDidTerminate:(WKWebView *)webView { NSLog(@"FFTagger WebKit process terminated"); [webView reload]; }
- (void)quit:(id)sender {
    [NSApp stop:nil];
    [NSApp postEvent:[NSEvent otherEventWithType:NSEventTypeApplicationDefined location:NSZeroPoint modifierFlags:0 timestamp:0 windowNumber:0 context:nil subtype:0 data1:0 data2:0] atStart:NO];
}
- (void)windowWillClose:(NSNotification *)notification { [self quit:nil]; }
- (NSApplicationTerminateReply)applicationShouldTerminate:(NSApplication *)sender { [self quit:nil]; return NSTerminateCancel; }
- (void)webView:(WKWebView *)webView decidePolicyForNavigationAction:(WKNavigationAction *)action decisionHandler:(void (^)(WKNavigationActionPolicy))handler {
    NSURL *url = action.request.URL;
    BOOL local = [url.scheme isEqualToString:@"http"] && [url.host isEqualToString:self.host] && [url.port isEqualToNumber:self.port];
    handler(local ? WKNavigationActionPolicyAllow : WKNavigationActionPolicyCancel);
}
- (void)webView:(WKWebView *)webView runOpenPanelWithParameters:(WKOpenPanelParameters *)parameters initiatedByFrame:(WKFrameInfo *)frame completionHandler:(void (^)(NSArray<NSURL *> *))handler {
    NSOpenPanel *panel = [NSOpenPanel openPanel];
    panel.canChooseFiles = YES; panel.canChooseDirectories = NO; panel.allowsMultipleSelection = NO;
    [panel beginSheetModalForWindow:self.window completionHandler:^(NSModalResponse response) { handler(response == NSModalResponseOK ? panel.URLs : nil); }];
}
@end

static FFTaggerDelegate *delegate;
void fftagger_set_theme(int mode) {
    dispatch_async(dispatch_get_main_queue(), ^{
        NSApp.appearance = mode == 2 ? [NSAppearance appearanceNamed:NSAppearanceNameDarkAqua] : mode == 1 ? [NSAppearance appearanceNamed:NSAppearanceNameAqua] : nil;
    });
}
void fftagger_run(const char *address) {
    @autoreleasepool {
        [NSApplication sharedApplication];
        [NSApp setActivationPolicy:NSApplicationActivationPolicyRegular];
        delegate = [[FFTaggerDelegate alloc] init];
        [NSApp setDelegate:delegate];
        NSURL *url = [NSURL URLWithString:[NSString stringWithUTF8String:address]];
        delegate.host = url.host; delegate.port = url.port;
        NSMenu *menu = [[NSMenu alloc] init];
        NSMenuItem *appItem = [[NSMenuItem alloc] init]; [menu addItem:appItem];
        NSMenu *appMenu = [[NSMenu alloc] initWithTitle:@"FFTagger"];
        NSMenuItem *quit = [[NSMenuItem alloc] initWithTitle:@"Завершить FFTagger" action:@selector(quit:) keyEquivalent:@"q"];
        quit.target = delegate; [appMenu addItem:quit]; appItem.submenu = appMenu;
        NSMenuItem *editItem = [[NSMenuItem alloc] initWithTitle:@"Правка" action:nil keyEquivalent:@""];
        NSMenu *editMenu = [[NSMenu alloc] initWithTitle:@"Правка"];
        [editMenu addItemWithTitle:@"Отменить" action:@selector(undo:) keyEquivalent:@"z"];
        [editMenu addItemWithTitle:@"Вырезать" action:@selector(cut:) keyEquivalent:@"x"];
        [editMenu addItemWithTitle:@"Скопировать" action:@selector(copy:) keyEquivalent:@"c"];
        [editMenu addItemWithTitle:@"Вставить" action:@selector(paste:) keyEquivalent:@"v"];
        [editMenu addItemWithTitle:@"Выбрать всё" action:@selector(selectAll:) keyEquivalent:@"a"];
        editItem.submenu = editMenu; [menu addItem:editItem]; [NSApp setMainMenu:menu];
        delegate.window = [[NSWindow alloc] initWithContentRect:NSMakeRect(0,0,1320,850) styleMask:NSWindowStyleMaskTitled|NSWindowStyleMaskClosable|NSWindowStyleMaskMiniaturizable|NSWindowStyleMaskResizable backing:NSBackingStoreBuffered defer:NO];
        delegate.window.title = @"FFTagger"; delegate.window.minSize = NSMakeSize(980,650);
        delegate.window.delegate = delegate; delegate.window.releasedWhenClosed = NO;
        WKWebViewConfiguration *configuration = [[WKWebViewConfiguration alloc] init];
        configuration.websiteDataStore = [WKWebsiteDataStore nonPersistentDataStore];
        delegate.web = [[WKWebView alloc] initWithFrame:delegate.window.contentView.bounds configuration:configuration];
        delegate.web.autoresizingMask = NSViewWidthSizable|NSViewHeightSizable;
        delegate.web.navigationDelegate = delegate; delegate.web.UIDelegate = delegate;
        [delegate.window setContentView:delegate.web];
        [delegate.web loadRequest:[NSURLRequest requestWithURL:url]];
        [delegate.window center]; [delegate.window makeKeyAndOrderFront:nil]; [NSApp activateIgnoringOtherApps:YES];
        [NSApp run];
        delegate.web.navigationDelegate = nil; delegate.web.UIDelegate = nil;
        [delegate.window orderOut:nil];
    }
}
char *fftagger_choose_folder(void) {
    __block char *result = NULL;
    dispatch_sync(dispatch_get_main_queue(), ^{
        NSOpenPanel *panel = [NSOpenPanel openPanel];
        panel.title = @"Выберите папку с музыкой"; panel.prompt = @"Сканировать";
        panel.canChooseFiles = NO; panel.canChooseDirectories = YES; panel.allowsMultipleSelection = NO;
        if ([panel runModal] == NSModalResponseOK) { result = strdup(panel.URL.path.UTF8String); }
    });
    return result;
}
