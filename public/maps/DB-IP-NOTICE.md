# IP Geolocation by DB-IP

DB-IP City Lite and Country Lite, September 2026.
Source: https://db-ip.com/db/lite.php
Copyright DB-IP.com. Licensed under Creative Commons Attribution 4.0 International:
https://creativecommons.org/licenses/by/4.0/

The databases are downloaded during the API image build, pinned by SHA-256 in
Dockerfile.api and used locally without modification (gzip is decompressed for MMDB).
No traffic addresses or payloads are sent to DB-IP. The download server receives
the build machine's connection, not traffic history. City/region coordinates are
approximate IP-block estimates, not verified physical machine positions. Anycast,
VPNs, CDN routing and database age can produce discrepancies.

NERVE derives country-only map representatives separately from public-domain
Natural Earth data; these are not DB-IP city coordinates. The Map attributes DB-IP
with a link wherever its data is displayed, including external ntopng mode.
