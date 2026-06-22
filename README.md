# forti-internalformat

Code and documentation for Forti's internal data format. This document is intended for developers who need to read or write the format directly.

## Overview

Forti's internal format is used for storing forecast data in blob storage and is consumed by `rawdataforecaster`.

Data is organized into **areas** and **versions**:

- An **area** refers to a limited geographic region, typically the domain of a single forecast model.
- A **version** number identifies which forecast for an area is the newest — the highest version number wins.

A single `rawdataforecaster` instance is supposed to serve data from several areas, but only one area per request. It will select the area whose grid point is closest to the requested location, and always the latest version for that area.

Within an area, data is expressed as one or more lists of latitude/longitude values with accompanying forecast parameter values. Multiple lat/lon lists may exist to allow different parameters to have different resolutions while still covering the same geographic area.

All data for a given area/version lives under `/<area>/<version>/` in blob storage.

## Object store layout

```
/<area>/<version>/
  complete.json
  <subfolder>/
    meta.json
    latitude
    longitude
    data
  <subfolder>/
    ...
```

### `complete.json`

Metadata about the area/version as a whole. Its structure is described by `DatasetMeta` in [`collector.go`](collector.go).

> **Important:** This file should be uploaded last. Its presence is supposed to trigger an update in `rawdataforecaster`.

### Subfolders

Each subfolder represents a distinct resolution (a unique set of lat/lon values). The subfolder name must be unique per lat/lon list — for example, the MD5 hash of the concatenated latitude and longitude arrays.

#### `meta.json`

Describes the meaning of the forecast values. Structure is defined by `MetaCollection` in [`collector.go`](collector.go). See [`collector_test.go`](collector_test.go) for an example.

#### `latitude` and `longitude`

Each file contains the lat or lon of every forecast point, binary-encoded as `float32` values. The ordering across the two files and the `data` file must be consistent, but the order itself does not matter.

#### `data`

Contains the actual forecast values as a series of little-endian `int16` values. Their meaning is defined in `meta.json`.

To look up data for a specific location:

1. Find the index of the desired point in the `latitude`/`longitude` arrays.
2. Multiply that index by `number_of_points` (from the metadata) to get the starting offset in `data`.
3. Read `number_of_points` values from that offset and interpret them using the metadata.

See [`collector_test.go`](collector_test.go) for a working example.
