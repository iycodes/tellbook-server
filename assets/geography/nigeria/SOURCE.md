# Nigeria administrative boundary data

Downloaded: 2026-08-20

## Contents

| File | Administrative level | Features | Geometry types |
| --- | --- | ---: | --- |
| `geoBoundaries-NGA-ADM1.geojson` | State/FCT (ADM1) | 37 | Polygon |
| `geoBoundaries-NGA-ADM2.geojson` | LGA/FCT Area Council (ADM2) | 774 | Polygon, MultiPolygon |

The adjacent `*.metadata.json` files are the geoBoundaries API metadata captured at download time. `LICENSE-CC-BY-4.0.txt` is a copy of the applicable Creative Commons legal code.

## Sources

- ADM1 metadata API: <https://www.geoboundaries.org/api/current/gbOpen/NGA/ADM1/>
- ADM2 metadata API: <https://www.geoboundaries.org/api/current/gbOpen/NGA/ADM2/>
- ADM1 GeoJSON: <https://github.com/wmgeolab/geoBoundaries/raw/9469f09/releaseData/gbOpen/NGA/ADM1/geoBoundaries-NGA-ADM1.geojson>
- ADM2 GeoJSON: <https://github.com/wmgeolab/geoBoundaries/raw/9469f09/releaseData/gbOpen/NGA/ADM2/geoBoundaries-NGA-ADM2.geojson>
- Underlying source: GRID3 Nigeria Operational State and LGA Boundaries, produced by eHealth Africa and Proxy Logics.

The URLs are pinned to geoBoundaries release commit `9469f09` so that the imported geometry is reproducible. The captured metadata identifies the represented boundary year as 2022 and the original boundary source as GRID3.

## Licence and attribution

The captured metadata identifies both files as Creative Commons Attribution 4.0 International (CC BY 4.0): <https://creativecommons.org/licenses/by/4.0/>.

Required attribution:

> Nigeria State and Local Government Area boundaries from GRID3 Nigeria Operational Boundaries, © eHealth Africa and Proxy Logics, distributed through geoBoundaries under CC BY 4.0.

If Tellbook repairs, simplifies, aggregates, or otherwise modifies the geometry, the public attribution must also indicate that changes were made.

These are operational boundaries and must not be described as authoritative legal boundary determinations.

## Integrity

```text
64fa218ac3d453cc1e66412ff461c5dfa1a4a1ade0da93b239a9891b587d28f9  geoBoundaries-NGA-ADM1.geojson
bef7f2cfa45e012f4772eaa61c7b99e5188aeba4d5c6badae7e9f9aae8c02fcd  geoBoundaries-NGA-ADM2.geojson
3fdeb3fa558a803da11dbfc957319b850b0b0ce9f47b6c374f25b12f6a6d03a4  geoBoundaries-NGA-ADM1.metadata.json
eaa2342efbad55a3ca9967611670f7001e80e000ef8c180d2c3d5f2eb03e14da  geoBoundaries-NGA-ADM2.metadata.json
9ba9550ad48438d0836ddab3da480b3b69ffa0aac7b7878b5a0039e7ab429411  LICENSE-CC-BY-4.0.txt
```

Both GeoJSON files parsed successfully as `FeatureCollection` values, contained the expected feature counts, and contained no null geometries at download time. Full PostGIS validity and topology checks should run during the database import.

## Import note

The ADM2 file does not include an explicit parent-state identifier. Assign each LGA to an ADM1 state during import using a representative point and a spatial containment/coverage join, then review boundary-edge exceptions before publishing the normalized region hierarchy.
