import type { CSSProperties, SVGProps } from 'react';

const starPath =
    'M40.57,10.55l4.57,9.26c.74,1.51,2.18,2.55,3.84,2.79l10.22,1.48c4.18.61,5.85,5.75,2.83,8.7l-7.39,7.21c-1.2,1.17-1.75,2.86-1.47,4.51l1.75,10.18c.71,4.17-3.66,7.34-7.4,5.38l-9.14-4.8c-1.49-.78-3.26-.78-4.75,0l-9.14,4.8c-3.74,1.97-8.11-1.21-7.4-5.38l1.75-10.18c.28-1.65-.26-3.34-1.47-4.51l-7.39-7.21c-3.03-2.95-1.36-8.09,2.83-8.7l10.22-1.48c1.66-.24,3.1-1.28,3.84-2.79l4.57-9.26c1.87-3.79,7.28-3.79,9.15,0Z';

const fillStyle: CSSProperties = {
    fill: 'var(--icon-fill, rgba(0, 0, 0, 0))',
    opacity: 'var(--icon-fill-opacity, 1)',
    strokeWidth: 0,
};

const strokeStyle: CSSProperties = {
    fill: 'none',
    stroke: 'var(--icon-stroke-color, currentColor)',
    strokeMiterlimit: 10,
    strokeWidth: 'var(--icon-stroke-width-m, calc(var(--icon-stroke-width, 5) * 1))',
};

const secondaryStrokeStyle: CSSProperties = {
    fill: 'none',
    stroke: 'var(--icon-stroke-color-secondary, var(--icon-stroke-color, currentColor))',
    strokeLinecap: 'round',
    strokeLinejoin: 'round',
    strokeWidth:
        'var(--icon-stroke-width-secondary-m, var(--icon-stroke-width-m, calc(var(--icon-stroke-width, 5) * 1)))',
};

const secondaryDashedStrokeStyle: CSSProperties = {
    ...secondaryStrokeStyle,
    strokeDasharray: '0 0 0 5.24',
};

export default function FaviconIcon({ className, style, ...rest }: SVGProps<SVGSVGElement>) {
    return (
        <svg
            id='sea-star'
            xmlns='http://www.w3.org/2000/svg'
            viewBox='0 0 72 72'
            aria-hidden='true'
            className={className}
            style={style}
            {...rest}
        >
            <path d={starPath} style={fillStyle} />
            <path d={starPath} style={strokeStyle} />
            <g>
                <line x1='36' y1='36' x2='36' y2='36' style={secondaryStrokeStyle} />
                <line x1='32.92' y1='40.24' x2='28.3' y2='46.6' style={secondaryDashedStrokeStyle} />
                <line x1='26.76' y1='48.72' x2='26.76' y2='48.72' style={secondaryStrokeStyle} />
            </g>
            <g>
                <line x1='50.95' y1='31.14' x2='50.95' y2='31.14' style={secondaryStrokeStyle} />
                <line x1='45.97' y1='32.76' x2='38.49' y2='35.19' style={secondaryDashedStrokeStyle} />
                <line x1='36' y1='36' x2='36' y2='36' style={secondaryStrokeStyle} />
                <line x1='39.08' y1='40.24' x2='43.7' y2='46.6' style={secondaryDashedStrokeStyle} />
                <line x1='45.24' y1='48.72' x2='45.24' y2='48.72' style={secondaryStrokeStyle} />
            </g>
            <g>
                <line x1='36' y1='20.28' x2='36' y2='20.28' style={secondaryStrokeStyle} />
                <line x1='36' y1='25.52' x2='36' y2='33.38' style={secondaryDashedStrokeStyle} />
                <line x1='36' y1='36' x2='36' y2='36' style={secondaryStrokeStyle} />
            </g>
            <g>
                <line x1='21.05' y1='31.14' x2='21.05' y2='31.14' style={secondaryStrokeStyle} />
                <line x1='26.03' y1='32.76' x2='33.51' y2='35.19' style={secondaryDashedStrokeStyle} />
                <line x1='36' y1='36' x2='36' y2='36' style={secondaryStrokeStyle} />
            </g>
        </svg>
    );
}
