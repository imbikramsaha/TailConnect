#import <Cocoa/Cocoa.h>

extern void tcReopen(void);

@interface TCReopenHandler : NSObject
@end

@implementation TCReopenHandler
- (void)handleReopen:(NSAppleEventDescriptor *)event withReplyEvent:(NSAppleEventDescriptor *)reply {
  tcReopen();
}
@end

static TCReopenHandler *handler;

void tcInstallReopenHandler(void) {
  dispatch_async(dispatch_get_main_queue(), ^{
    handler = [TCReopenHandler new];
    // kCoreEventClass 'aevt', kAEReopenApplication 'rapp'
    [[NSAppleEventManager sharedAppleEventManager] setEventHandler:handler
                                                       andSelector:@selector(handleReopen:withReplyEvent:)
                                                     forEventClass:'aevt'
                                                        andEventID:'rapp'];
  });
}
