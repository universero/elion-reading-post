#!/bin/bash
CURDIR=$(cd $(dirname $0); pwd)
BinaryName=elion-reading-post
echo "$CURDIR/bin/${BinaryName}"
exec $CURDIR/bin/${BinaryName}