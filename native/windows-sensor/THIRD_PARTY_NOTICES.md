# Windows sensor dependencies

The sensor uses Windows' built-in ETW facilities; no Npcap, WinDivert, packet
injection driver or third-party kernel driver is installed.

Microsoft.Diagnostics.Tracing.TraceEvent 3.2.6 is from Microsoft's PerfView
repository: https://github.com/microsoft/perfview (MIT).
System.ServiceProcess.ServiceController and the self-contained .NET runtime are
from https://github.com/dotnet/runtime (MIT). Runtime distribution notices are
included in the publish output. NuGet packages come only from nuget.org; exact
versions and SHA-512 content hashes are recorded in packages.lock.json and restore
uses locked mode. The official Microsoft SDK image is pinned by SHA-256 digest.
Unused proprietary `KernelTraceControl.dll` and `msdia140.dll`, plus DIA symbol
helpers, are explicitly excluded from the published artifact. Capture uses only
the managed TraceEvent parser and built-in Windows ETW/TDH APIs. The .NET SDK
distribution's MIT license and third-party notices are copied into the artifact.

## Microsoft PerfView license

Copyright (c) Microsoft Corporation. All rights reserved.

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.

## Offline IP country data (API container)

DB-IP Country Lite, September 2026, https://db-ip.com/db/download/ip-to-country-lite,
is distributed under CC BY 4.0: https://creativecommons.org/licenses/by/4.0/.
The API image downloads the official CSV gzip at build time and verifies its
SHA-256 (see Dockerfile.api). No IP-address lookup requests leave the running API.
The Map attributes DB-IP and combines country codes with the existing Natural
Earth country centroids; this is an approximate country location, not a host's
physical location. No changes are made to the downloaded dataset.
