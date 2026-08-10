import type { FeatureCollection, MultiPolygon, Polygon, Position } from 'geojson';
import type { GeometryCollection, Topology } from 'topojson-specification';
import type { DistributionItem } from '@/api';

import { numericToAlpha2 } from 'i18n-iso-countries';
import { Globe2 } from 'lucide-react';
import { useMemo, useState } from 'react';
import { feature } from 'topojson-client';
import worldTopology from 'world-atlas/countries-110m.json';

import { ContentCard } from '@/components/ContentCard.tsx';
import { countryOptions } from '@/data/countries.ts';

type Period = '24h' | '30d';

interface GeoTrafficPanelProps {
    items: DistributionItem[];
    period: Period;
    loading?: boolean;
    title?: string;
}

interface CountryTraffic extends DistributionItem {
    code: string;
    name: string;
    traffic: number;
}

interface MapLocation {
    id: string;
    name: string;
    path: string;
}

interface AtlasProperties {
    name: string;
}

const countryNames = new Map(countryOptions.map((country) => [country.id, country.name]));
countryNames.set('XK', 'Kosovo');
const loadingRows = [
    'loading-country-1',
    'loading-country-2',
    'loading-country-3',
    'loading-country-4',
    'loading-country-5',
];

function pointToPath([longitude, latitude]: Position) {
    return `${(longitude + 180).toFixed(2)} ${(90 - latitude).toFixed(2)}`;
}

function polygonToPath(polygon: Position[][]) {
    return polygon
        .filter((ring) => ring.length > 0)
        .map((ring) => `M${ring.map(pointToPath).join('L')}Z`)
        .join('');
}

function geometryToPath(geometry: Polygon | MultiPolygon) {
    return geometry.type === 'Polygon'
        ? polygonToPath(geometry.coordinates)
        : geometry.coordinates.map(polygonToPath).join('');
}

const atlas = feature<AtlasProperties>(
    worldTopology as unknown as Topology,
    worldTopology.objects.countries as unknown as GeometryCollection<AtlasProperties>
) as unknown as FeatureCollection<Polygon | MultiPolygon, AtlasProperties>;

const mapLocations: MapLocation[] = atlas.features.flatMap((country) => {
    const code =
        numericToAlpha2(String(country.id).padStart(3, '0')) ??
        (country.properties.name === 'Kosovo' ? 'XK' : undefined);
    if (!code) return [];
    return [
        {
            id: code.toLowerCase(),
            name: country.properties.name,
            path: geometryToPath(country.geometry),
        },
    ];
});

function formatBytes(bytes: number) {
    if (!Number.isFinite(bytes) || bytes <= 0) return '0 B';
    const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
    const unit = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1);
    return `${(bytes / 1024 ** unit).toFixed(unit === 0 ? 0 : 1)} ${units[unit]}`;
}

function mergeCountries(items: DistributionItem[]) {
    const merged = new Map<string, DistributionItem>();
    for (const item of items) {
        const code = item.value.trim().toUpperCase();
        if (!code) continue;
        const current = merged.get(code) ?? {
            value: code,
            requests: 0,
            ingress_bytes: 0,
            egress_bytes: 0,
        };
        current.requests += item.requests;
        current.ingress_bytes += item.ingress_bytes;
        current.egress_bytes += item.egress_bytes;
        merged.set(code, current);
    }
    return Array.from(
        merged,
        ([code, item]): CountryTraffic => ({
            ...item,
            code,
            name: countryNames.get(code) ?? code,
            traffic: item.ingress_bytes + item.egress_bytes,
        })
    ).sort((left, right) => right.traffic - left.traffic);
}

export function GeoTrafficPanel({
    items,
    period,
    loading = false,
    title = 'Traffic by country',
}: GeoTrafficPanelProps) {
    const [activeCode, setActiveCode] = useState<string | null>(null);
    const countries = useMemo(() => mergeCountries(items), [items]);
    const byCode = useMemo(
        () => new Map(countries.map((country) => [country.code.toLowerCase(), country])),
        [countries]
    );
    const totalTraffic = countries.reduce((sum, country) => sum + country.traffic, 0);
    const maxTraffic = countries[0]?.traffic ?? 0;
    const topCountries = countries.slice(0, 5);
    const activeCountry = activeCode ? byCode.get(activeCode.toLowerCase()) : undefined;
    const highlightedCountry = activeCountry ?? topCountries[0];

    return (
        <ContentCard
            noPadding
            action={<span className='text-xs font-medium text-muted'>{period}</span>}
            title={title}
        >
            <div
                aria-busy={loading || undefined}
                className='grid min-h-[330px] lg:grid-cols-[minmax(0,1fr)_310px]'
            >
                <div className='relative flex min-h-[260px] items-center justify-center overflow-hidden border-b border-border bg-surface-secondary/15 px-3 py-4 lg:min-h-[390px] lg:border-b-0 lg:border-r lg:px-6'>
                    {loading && countries.length === 0 ? (
                        <div className='h-full min-h-[240px] w-full animate-pulse rounded-lg bg-surface-secondary/60 lg:min-h-[340px]' />
                    ) : (
                        <>
                            <svg
                                aria-label={`${period} traffic by country map`}
                                className='h-auto max-h-[390px] w-full text-muted'
                                preserveAspectRatio='xMidYMid meet'
                                role='img'
                                viewBox='0 0 360 180'
                            >
                                {mapLocations.map((location) => {
                                    const country = byCode.get(location.id);
                                    const isActive = activeCode === location.id;
                                    if (!country) {
                                        return (
                                            <path
                                                className='fill-current'
                                                d={location.path}
                                                key={location.id}
                                                stroke='var(--color-surface)'
                                                strokeWidth={0.7}
                                                style={{ fillOpacity: 0.12 }}
                                            >
                                                <title>{location.name}: no traffic</title>
                                            </path>
                                        );
                                    }
                                    const intensity =
                                        maxTraffic > 0
                                            ? 0.28 + Math.sqrt(country.traffic / maxTraffic) * 0.72
                                            : 0.28;
                                    const label = `${country.name}: ${formatBytes(country.traffic)}, ${country.requests.toLocaleString()} requests`;
                                    return (
                                        // biome-ignore lint/a11y/useSemanticElements: An interactive SVG region cannot use an HTML button element.
                                        <path
                                            aria-label={label}
                                            className='cursor-pointer fill-primary transition-[fill-opacity,stroke] duration-150 focus:outline-none focus-visible:stroke-foreground'
                                            d={location.path}
                                            key={location.id}
                                            role='button'
                                            stroke={
                                                isActive
                                                    ? 'var(--color-foreground)'
                                                    : 'var(--color-surface)'
                                            }
                                            strokeWidth={isActive ? 1.6 : 0.7}
                                            style={{ fillOpacity: isActive ? 1 : intensity }}
                                            tabIndex={0}
                                            onBlur={() => setActiveCode(null)}
                                            onFocus={() => setActiveCode(location.id)}
                                            onPointerEnter={() => setActiveCode(location.id)}
                                            onPointerLeave={() => setActiveCode(null)}
                                        >
                                            <title>{label}</title>
                                        </path>
                                    );
                                })}
                            </svg>
                            {highlightedCountry && (
                                <div className='pointer-events-none absolute bottom-3 left-3 rounded-lg border border-border/70 bg-surface/95 px-3 py-2 shadow-sm backdrop-blur-sm sm:bottom-4 sm:left-4'>
                                    <div className='flex items-baseline gap-2'>
                                        <span className='text-sm font-semibold'>
                                            {highlightedCountry.name}
                                        </span>
                                        <span className='text-[11px] font-medium text-muted'>
                                            {highlightedCountry.code}
                                        </span>
                                    </div>
                                    <div className='mt-1 flex gap-3 text-xs text-muted'>
                                        <span>{formatBytes(highlightedCountry.traffic)}</span>
                                        <span>
                                            {highlightedCountry.requests.toLocaleString()} requests
                                        </span>
                                    </div>
                                </div>
                            )}
                            {!loading && countries.length === 0 && (
                                <div className='absolute inset-0 flex items-center justify-center bg-surface/35'>
                                    <div className='flex flex-col items-center px-6 text-center'>
                                        <Globe2 className='h-6 w-6 text-muted' />
                                        <p className='mt-2 text-sm font-medium'>No country data</p>
                                        <p className='mt-1 text-xs text-muted'>
                                            No visits were geolocated in this period.
                                        </p>
                                    </div>
                                </div>
                            )}
                        </>
                    )}
                </div>

                <div className='flex min-w-0 flex-col px-4 py-4'>
                    <div className='flex items-center justify-between gap-3'>
                        <span className='text-xs font-semibold uppercase tracking-wide text-muted'>
                            Top countries
                        </span>
                        <span className='text-xs text-muted'>{countries.length} regions</span>
                    </div>
                    <div className='mt-2 flex-1 divide-y divide-border/70'>
                        {loading && countries.length === 0
                            ? loadingRows.map((row) => (
                                  <div
                                      className='flex animate-pulse items-center gap-3 py-3'
                                      key={row}
                                  >
                                      <div className='h-7 w-7 rounded-md bg-surface-secondary' />
                                      <div className='h-3 flex-1 rounded bg-surface-secondary' />
                                      <div className='h-3 w-14 rounded bg-surface-secondary' />
                                  </div>
                              ))
                            : topCountries.map((country, index) => {
                                  const share =
                                      totalTraffic > 0 ? country.traffic / totalTraffic : 0;
                                  const isActive = activeCode?.toUpperCase() === country.code;
                                  return (
                                      <button
                                          className={`grid w-full grid-cols-[28px_minmax(0,1fr)_auto] items-center gap-2.5 py-3 text-left transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-primary ${isActive ? 'text-primary' : ''}`}
                                          key={country.code}
                                          type='button'
                                          onBlur={() => setActiveCode(null)}
                                          onFocus={() => setActiveCode(country.code.toLowerCase())}
                                          onPointerEnter={() =>
                                              setActiveCode(country.code.toLowerCase())
                                          }
                                          onPointerLeave={() => setActiveCode(null)}
                                      >
                                          <span className='text-xs font-semibold tabular-nums text-muted'>
                                              {String(index + 1).padStart(2, '0')}
                                          </span>
                                          <span className='min-w-0'>
                                              <span className='flex min-w-0 items-baseline gap-1.5'>
                                                  <span className='truncate text-sm font-medium'>
                                                      {country.name}
                                                  </span>
                                                  <span className='shrink-0 text-[10px] font-medium text-muted'>
                                                      {country.code}
                                                  </span>
                                              </span>
                                              <span className='mt-0.5 block text-[11px] text-muted'>
                                                  {country.requests.toLocaleString()} requests
                                              </span>
                                          </span>
                                          <span className='text-right'>
                                              <span className='block text-sm font-semibold tabular-nums'>
                                                  {formatBytes(country.traffic)}
                                              </span>
                                              <span className='mt-0.5 block text-[11px] tabular-nums text-muted'>
                                                  {(share * 100).toFixed(share >= 0.1 ? 0 : 1)}%
                                              </span>
                                          </span>
                                      </button>
                                  );
                              })}
                    </div>
                    {!loading && countries.length === 0 && (
                        <div className='flex flex-1 items-center justify-center py-8 text-sm text-muted'>
                            No ranked countries
                        </div>
                    )}
                    {countries.length > 0 && (
                        <div className='mt-3 flex items-center justify-between border-t border-border pt-3 text-xs text-muted'>
                            <span>Reported traffic</span>
                            <span className='font-medium tabular-nums text-foreground'>
                                {formatBytes(totalTraffic)}
                            </span>
                        </div>
                    )}
                </div>
            </div>
        </ContentCard>
    );
}
