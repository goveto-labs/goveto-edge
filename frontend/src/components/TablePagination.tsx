import { Pagination } from '@heroui/react';

import { pageRange, visiblePages } from '@/utils/pagination.ts';

interface TablePaginationProps {
    page: number;
    pageSize: number;
    total: number;
    onPageChange: (page: number) => void;
}

export function TablePagination({ page, pageSize, total, onPageChange }: TablePaginationProps) {
    if (total <= 0) return null;
    const { page: safePage, pageCount, start, end } = pageRange(page, pageSize, total);
    return (
        <Pagination className='justify-between' size='sm'>
            <Pagination.Summary>
                Showing {start.toLocaleString()}-{end.toLocaleString()} of {total.toLocaleString()}
            </Pagination.Summary>
            <Pagination.Content>
                <Pagination.Item>
                    <Pagination.Previous
                        isDisabled={safePage <= 1}
                        onPress={() => onPageChange(Math.max(1, safePage - 1))}
                    >
                        <Pagination.PreviousIcon />
                        Previous
                    </Pagination.Previous>
                </Pagination.Item>
                {visiblePages(safePage, pageCount).map((item) =>
                    typeof item === 'number' ? (
                        <Pagination.Item
                            className={item === safePage ? undefined : 'hidden sm:block'}
                            key={item}
                        >
                            <Pagination.Link
                                isActive={item === safePage}
                                onPress={() => onPageChange(item)}
                            >
                                {item}
                            </Pagination.Link>
                        </Pagination.Item>
                    ) : (
                        <Pagination.Item className='hidden sm:block' key={item}>
                            <Pagination.Ellipsis />
                        </Pagination.Item>
                    )
                )}
                <Pagination.Item>
                    <Pagination.Next
                        isDisabled={safePage >= pageCount}
                        onPress={() => onPageChange(Math.min(pageCount, safePage + 1))}
                    >
                        Next
                        <Pagination.NextIcon />
                    </Pagination.Next>
                </Pagination.Item>
            </Pagination.Content>
        </Pagination>
    );
}
