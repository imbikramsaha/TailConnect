#import <Foundation/Foundation.h>
#import <UserNotifications/UserNotifications.h>

extern void tcNotifyClicked(char *route);

@interface TCNotifyDelegate : NSObject <UNUserNotificationCenterDelegate>
@end

@implementation TCNotifyDelegate
- (void)userNotificationCenter:(UNUserNotificationCenter *)center
       willPresentNotification:(UNNotification *)notification
         withCompletionHandler:(void (^)(UNNotificationPresentationOptions))completionHandler {
  completionHandler(UNNotificationPresentationOptionBanner | UNNotificationPresentationOptionList);
}
- (void)userNotificationCenter:(UNUserNotificationCenter *)center
didReceiveNotificationResponse:(UNNotificationResponse *)response
         withCompletionHandler:(void (^)(void))completionHandler {
  NSString *route = response.notification.request.content.userInfo[@"route"];
  tcNotifyClicked((char *)[(route ?: @"") UTF8String]);
  completionHandler();
}
@end

static TCNotifyDelegate *tcDelegate;
static BOOL tcAuthRequested;

int tcNotifyAvailable(void) {
  // UNUserNotificationCenter throws outside an app bundle.
  return [[NSBundle mainBundle] bundleIdentifier] != nil ? 1 : 0;
}

void tcNotify(const char *title, const char *body, const char *route) {
  NSString *t = [NSString stringWithUTF8String:title];
  NSString *b = [NSString stringWithUTF8String:body];
  NSString *r = [NSString stringWithUTF8String:route];
  UNUserNotificationCenter *center = [UNUserNotificationCenter currentNotificationCenter];
  if (!tcDelegate) {
    tcDelegate = [TCNotifyDelegate new];
    center.delegate = tcDelegate;
  }
  void (^post)(void) = ^{
    UNMutableNotificationContent *c = [UNMutableNotificationContent new];
    c.title = t;
    c.body = b;
    c.userInfo = @{@"route": r};
    c.sound = [UNNotificationSound defaultSound];
    UNNotificationRequest *req = [UNNotificationRequest requestWithIdentifier:[[NSUUID UUID] UUIDString]
                                                                      content:c trigger:nil];
    [center addNotificationRequest:req withCompletionHandler:nil];
  };
  if (!tcAuthRequested) {
    tcAuthRequested = YES;
    [center requestAuthorizationWithOptions:(UNAuthorizationOptionAlert | UNAuthorizationOptionSound)
                          completionHandler:^(BOOL granted, NSError *error) { if (granted) post(); }];
    return;
  }
  post();
}
