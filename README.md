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

And you can just display the best mirror for by `sudo rankmirror-ng` or automatically
set it as your download source with `sudo rankmirror-ng -set=true`. 
