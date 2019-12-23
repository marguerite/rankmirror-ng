rankmirror-ng

------

[![Go Report Card](https://goreportcard.com/badge/github.com/marguerite/rankmirror-ng)](https://goreportcard.com/report/github.com/marguerite/rankmirror-ng)

Next Generation tool for openSUSE users to test and change download mirror.

It can score mirrors by combination of 5 standards:

* Physical distance
* Route levels
* Route time
* Ping speed
* Download speed

You can give each standard a weight based on your experience.

## Usage

* list available mirrors sorted by weight: `rankmirror-ng -list`
* update mirrors' metadata: `sudo rankmirror-ng -update` (must use `sudo`)
* set mirror to use: `sudo rankmirror-ng -set=Tuna` ("Tuna" is a name can be found by `rankmirror-ng -list`)


