import type { CSSProperties, SVGProps } from 'react';

const fillStyle: CSSProperties = {
    fill: 'var(--icon-fill, rgba(0, 0, 0, 0))',
    opacity: 'var(--icon-fill-opacity, 1)',
};

const strokeStyle: CSSProperties = {
    fill: 'none',
    stroke: 'var(--icon-stroke-color, currentColor)',
    strokeMiterlimit: 10,
    strokeWidth: 'var(--icon-stroke-width-m, calc(var(--icon-stroke-width, 5) * 1))',
};

export default function AnimalStepIcon({ className, style, ...rest }: SVGProps<SVGSVGElement>) {
    return (
        <svg
            id='animal-step'
            xmlns='http://www.w3.org/2000/svg'
            viewBox='0 0 72 72'
            aria-hidden='true'
            className={className}
            style={style}
            {...rest}
        >
            <g>
                <ellipse cx='45.25' cy='22.76' rx='6.75' ry='8.44' style={fillStyle} />
                <ellipse
                    cx='57.6'
                    cy='35.82'
                    rx='6.19'
                    ry='4.95'
                    transform='translate(-8.46 51.22) rotate(-45)'
                    style={fillStyle}
                />
                <path
                    d='M36,56.19c-9.94,0-18,5.44-18-4.5,0-9.94,8.06-18,18-18,9.94,0,18,8.06,18,18s-8.06,4.5-18,4.5Z'
                    style={fillStyle}
                />
                <ellipse cx='26.75' cy='22.76' rx='6.75' ry='8.44' style={fillStyle} />
                <ellipse
                    cx='14.4'
                    cy='35.82'
                    rx='4.95'
                    ry='6.19'
                    transform='translate(-21.11 20.67) rotate(-45)'
                    style={fillStyle}
                />
            </g>
            <g>
                <ellipse cx='45.25' cy='22.76' rx='6.75' ry='8.44' style={strokeStyle} />
                <ellipse
                    cx='57.6'
                    cy='35.82'
                    rx='6.19'
                    ry='4.95'
                    transform='translate(-8.46 51.22) rotate(-45)'
                    style={strokeStyle}
                />
                <path
                    d='M36,56.19c-9.94,0-18,5.44-18-4.5,0-9.94,8.06-18,18-18,9.94,0,18,8.06,18,18s-8.06,4.5-18,4.5Z'
                    style={strokeStyle}
                />
                <ellipse cx='26.75' cy='22.76' rx='6.75' ry='8.44' style={strokeStyle} />
                <ellipse
                    cx='14.4'
                    cy='35.82'
                    rx='4.95'
                    ry='6.19'
                    transform='translate(-21.11 20.67) rotate(-45)'
                    style={strokeStyle}
                />
            </g>
        </svg>
    );
}
