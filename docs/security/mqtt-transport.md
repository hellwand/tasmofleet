# Commands over MQTT: threat model and claims

Scope: how tasmofleet sends commands, settings writes and policy fixes to a device over its MQTT
broker instead of its HTTP API (`internal/transport`, `internal/mqttcmd`, spec 6.5 and 6.6).
Backup, restore and update over MQTT extend this document when they land. Findings are judged
against it: valid if they break a claim below or show a residual risk worse than stated.

## What changes compared with HTTP

- On the MQTT path only the broker protects the device (user, ACL, TLS). The device's web password
  plays no part; anyone who can publish to `cmnd/<topic>/#` can do what the console can do.
- Commands, settings writes and their replies travel through the broker. Secret-bearing writes are allowed, as
  over HTTP: a name cmdpolicy keeps secret (`WebPassword`, `MqttPassword`, `Password<n>`, `SSId<n>`,
  `RgxPassword`, `RgxSSId`, the keys of `MI32Key`/`MI32Keys` and `ZbConfig`, ...) or any command whose text carries a credential (`cmdpolicy.CarriesCredential`:
  userinfo in a URL such as `OtaUrl http://user:pass@host/...` (a token alone or an empty user too),
  `user:pass@host`, the login in `Sendmail`'s and `WebSend`'s bracket argument, `password=`), and from the console
  or a bulk command every command whose arguments the audit hides (`cmdpolicy.ShownInFull`). While HTTP works they
  go over HTTP (claim T11); over MQTT only while HTTP fails, and then in clear text on a `tcp://` broker for every
  client allowed to read `cmnd/#`. Nothing warns about that: use `ssl://` and broker ACLs.

## Claims

| #   | Claim                                                                                                                                                                                                                                                                       | Test                                                                                                     |
| --- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------- |
| T1  | `Status 0` is never published, in any spelling, alone or in a `Backlog`, and not by a command that runs other commands: `Json`, `Publish`, `Publish2`, `Rule` with a rule text, `RuleTimer`, `If`, `WebSend`, `WebQuery`, `DevGroupSend`, `LoRaCommand`, `Br`, `Script`, `UfsRun` are refused by the channel.              | `TestStatus0IsNeverPublished`, `TestCommandsThatRunOtherCommandsAreRefused`, `TestSimStatus0CannotBePublishedByAnExecutor` |
| T2  | A command is sent on the other transport inside one call only when the first provably never left: HTTP to MQTT after a refused connection, an address outside DEVICE_NETWORKS, a 401, SetOption128; MQTT to HTTP after the channel refused the command or could not publish it. A timeout, an unparsable answer or a dropped connection is never followed by a second send. | `TestFailsOverToMQTTOnlyWhenNothingWasSent`, `TestRefusedConnectionIsOneMQTTSend`, `TestNotSent`, `TestAWriteMQTTDidNotPublishIsSentOnceOverHTTP`, `TestPowerMQTTDidNotPublishIsSentOverHTTP`, `TestPowerFallbackThroughTheRouterStaysOnHTTP` |
| T3  | A published command is never sent again over HTTP, also while HTTP works (a write over MQTT first that nobody answered is an unknown outcome). A channel closed while a command waits reports an unknown outcome, not "not sent"; a closed channel publishes nothing. | `TestAFailedMQTTSendIsNeverRepeatedOverHTTP`, `TestATimedOutMQTTWriteIsNeverResent`, `TestPowerOverMQTTUnconfirmedDoesNotRetryOverHTTP`, `TestCloseWakesACommandInFlight`, `TestDoAfterCloseNeverPublishes` |
| T4  | Reads follow the read path, every other command the stricter write path (an unread GroupTopic of this or another device blocks writes). Calls without a device ID never go to MQTT.                                                                                       | `TestReadsAndWritesFollowTheirOwnPath`, `TestTargetWithoutDeviceNeverUsesMQTT`                            |
| T5  | An upgrade or restore command is sent over MQTT only by a job that pinned the path.                                                                                                                                                                                         | `TestUpgradeAndRestoreCommandsOnlyGoOverMQTTPinned`                                                      |
| T6  | The topic is checked again right before the publish; a changed topic, group topic, FullTopic or MQTT capability sends nothing.                                                                                                                                              | `TestTopicChangeBetweenSnapshotAndSendSendsNothing`                                                      |
| T7  | The reply of a credential-setting command is masked before anything shows or stores it. `FileDownload`, `FileUpload` and `Upload` (their payload carries the device's MQTT password) are refused for every role.                                                              | `TestSecretReplyIsScrubbedOverMQTT`, `TestFileTransferCommandsAreNeverSent`                              |
| T8  | A change that can cut off a device reached over MQTT only (Topic, FullTopic, GroupTopic, Mqtt*, `SetOption3 0`) asks for a confirmation that says so.                                                                                                                         | `TestMQTTOnlyNamesTheCutOffConsequence`, `TestConsoleMQTTOnlyDeviceAsksBeforeACutOffChange`              |
| T10 | A write that can cut off a device reached over MQTT only is published only with a person's confirmation (the console's); Make compliant and bulk never carry it. The failure of a write after a refused-API answer is not repeated over MQTT unless the memory already knew the device refuses the API. | `TestACutOffWriteNeedsAConfirmationOverMQTT`, `TestAPIRefusedFailsOverOnlyForReadsOrAKnownRefusal`, `TestApplyLeavesOutWhatCouldCutAnMQTTOnlyDeviceOff` |
| T9  | The audit says which transport carried a write (`via`).                                                                                                                                                                                                                      | `TestRunOverMQTTIsAuditedAndScrubbed`, `TestRunOfAStrictlyUsableDeviceGoesOverMQTTAndIsAudited`, `TestPowerFollowsTheStrictWriteRule`, `TestPowerAuditNamesTheTransportTheRouterUsed` |
| T11 | A write goes over MQTT first only when the device is usable there under the strict rule (own GroupTopic read, no other device's unread, topic exclusive, FullTopic ours, last will Online) and its MQTT reads are not going unanswered; the power toggle asks the same rule. While HTTP is not failing, these go over HTTP first: a change that can cut the device off from MQTT (HTTP's answer confirms it), a console or bulk command whose arguments the audit does not show in full (`cmdpolicy.ShownInFull`, the allowlist of `Redacted`: a secret can hide in any command a person types, inside `If`, in `BLEPair`'s PIN, in `Sendmail`'s login), a write of TasmoFleet's own with secret arguments (a secret name such as `WebPassword`, `Password<n>`, `SSId<n>`, `RgxPassword`, `RgxSSId`, `WiFiTest<n>`, the BLE bind keys of `MI32Key`/`MI32Keys<n>`, `ZbConfig` with the Zigbee network key, or a credential in the text: any userinfo in a URL, `Sendmail`'s or `WebSend`'s login, `password=`; alone or in any `Backlog` form; over MQTT it would cross the broker in clear text), a `Template` with a template (it restarts the device and was never tried over MQTT on a real one) and Make compliant (interim, until #606). The reply of a write with a credential only in its text is returned as sent (the OTA engine compares the OtaUrl echo). | `TestStrictlyUsableWritesGoOverMQTTFirst`, `TestConsoleWritesTheAuditHidesStayHTTPFirstWhileHTTPWorks`, `TestRunKeepsCommandsTheAuditHidesHTTPFirstWhileHTTPWorks`, `TestShownInFull`, `TestSecretWritesStayHTTPFirstWhileHTTPWorks`, `TestACredentialInTheTextLeavesTheReplyAsSent`, `TestCarriesCredential`, `TestWritesThatMQTTMustNotCarryFirstGoOverHTTP`, `TestRouterWritesFollowChoose`, `TestACutOffWriteGoesOverHTTPWhileHTTPWorks`, `TestTemplateWritesStayHTTPFirst`, `TestHTTPFirstWritesMarkerKeepsTodaysOrder`, `TestPowerFollowsTheStrictWriteRule`, `TestPowerAsksTheRoutersWriteRule` |

## Residual risk

- **Low.** A broker client that spoofs a device topic can answer a query (a reply is matched by topic and by
  the command's key, not by a signature). Identity checks (MAC via Status 5, family) still run before restore
  and update.
- **Low.** A write over MQTT first that the device did not confirm in time is an unknown outcome; the person sees it
  and decides (nothing resends it). A device whose replies do not reach TasmoFleet would leave every such write
  unknown; its reads go unanswered first and mark it, and a marked device is changed over HTTP for a while.
- **Low.** `GroupTopic2` to `GroupTopic4` are not stored; a device listening on one of them is not seen as
  sharing the topic. A device with a topic that neither HTTP nor our broker reaches keeps an unread
  GroupTopic and holds back MQTT writes for the others.
- **Low.** The reply scrub masks values by their JSON key name (password, token, SSId, ...) and, since #605, the
  exact keys `KeyL`, `KeyH` (`ZbConfig`) and `MIKeys` (`MI32Key`/`MI32Keys`); a key under any other name a device
  answers with reaches the admin who sent the command, over HTTP as over MQTT, as before #605. cmdpolicy, its
  policy rules, the reply scrub and the backup diff each decide by their own rule what is secret; #809 makes it
  one rule.
- **Low.** A write of TasmoFleet's own (settings page, power, Restart, a job) is judged by cmdpolicy's secret names
  and credential patterns, not by the audit's allowlist: its commands come from curated lists, so a secret name
  missing there would go MQTT first. The console and bulk commands, where a person types any text, use the
  allowlist and fail closed.
