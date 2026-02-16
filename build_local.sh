#!/bin/bash
set -e

BINARY_NAME="askflow"

echo "============================================"
echo " Askflow Local Build"
echo "============================================"
echo

echo "[1/2] Building ${BINARY_NAME}..."
go build -o "${BINARY_NAME}" .
echo "       Build OK"
echo

echo "[2/2] Build info:"
echo "       Binary: $(pwd)/${BINARY_NAME}"
echo "       Size:   $(du -h "${BINARY_NAME}" | cut -f1)"
echo

echo "============================================"
echo " Build complete! Run with: ./${BINARY_NAME}"
echo "============================================"
