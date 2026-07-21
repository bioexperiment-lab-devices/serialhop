package remoteupdate

import (
	"os"
	"time"
)

func nowRFC3339() string { return time.Now().UTC().Format(time.RFC3339) }

var osRemove = os.Remove
