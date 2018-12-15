# myq-homecontrol

HomeKit support for Chamberlain / LiftMaster MyQ garage doors using
[HomeControl](https://github.com/brutella/hc) and my [MyQ Go
library](https://github.com/joeshaw/myq).

When running, this service publishes a HomeKit garage door accessory.

After the door is paired with your iOS Home app, you can control it
with any service that integrates with HomeKit, including Siri,
Shortcuts, and the Apple Watch.  If you have a home hub like an Apple
TV or iPad, you can control the garage door remotely.

## Installing

The tool can be installed with:

    go get -u github.com/joeshaw/myq-homecontrol

You will need to create a `config.json` file with your MyQ username
and password, and your MyQ device ID

```json
{
    "username": "foo@example.com",
    "password": "myqPassw0rd",
    "device_id": "2613952120",
}
```

You can get your MyQ device ID using the `myq` command from my [MyQ
repo](https://github.com/joeshaw/myq) by running:

    myq -username <username> -password <password> devices

Then you can run the service:

    myq-homecontrol -config config.json

The service will call the MyQ API to get the current garage door
state, and update it every 15 minutes.

To pair, open up your Home iOS app, click the + icon, choose "Add
Accessory" and then tap "Don't have a Code or Can't Scan?"  You should
see the garage door under "Nearby Accessories."  Tap that and enter
the PIN 00102003 (or whatever you chose in `config.json`).  You should
see a garage door appear in your device list.

## Contributing

Issues and pull requests are welcome.  When filing a PR, please make
sure the code has been run through `gofmt`.

## License

Copyright 2018 Joe Shaw

`myq-homecontrol` is licensed under the MIT License.  See the LICENSE file
for details.


