module github.com/mailhog/storage
go 1.26

require github.com/mailhog/data v0.0.0
replace github.com/mailhog/data => ../data
require gopkg.in/mgo.v2 v2.0.0
replace gopkg.in/mgo.v2 => ../../gopkg.in/mgo.v2
