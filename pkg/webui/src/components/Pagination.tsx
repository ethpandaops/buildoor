import React from 'react';

interface PaginationProps {
  total: number;
  offset: number;
  limit: number;
  onPageChange: (newOffset: number) => void;
}

export const Pagination: React.FC<PaginationProps> = ({
  total,
  offset,
  limit,
  onPageChange,
}) => {
  const currentPage = Math.floor(offset / limit) + 1;
  const totalPages = Math.ceil(total / limit);

  if (totalPages <= 1) return null;

  const pages: (number | string)[] = [];
  const maxVisible = 5;

  if (totalPages <= maxVisible) {
    for (let i = 1; i <= totalPages; i++) {
      pages.push(i);
    }
  } else {
    pages.push(1);
    if (currentPage > 3) pages.push('...');
    for (let i = Math.max(2, currentPage - 1); i <= Math.min(totalPages - 1, currentPage + 1); i++) {
      pages.push(i);
    }
    if (currentPage < totalPages - 2) pages.push('...');
    pages.push(totalPages);
  }

  return (
    <nav className="d-flex justify-content-center mt-3">
      <ul className="pagination mb-0">
        <li className={`page-item ${offset === 0 ? 'disabled' : ''}`}>
          <button
            className="page-link"
            onClick={() => onPageChange(Math.max(0, offset - limit))}
            disabled={offset === 0}
          >
            Previous
          </button>
        </li>

        {pages.map((page, idx) =>
          typeof page === 'number' ? (
            <li key={idx} className={`page-item ${page === currentPage ? 'active' : ''}`}>
              <button
                className="page-link"
                onClick={() => onPageChange((page - 1) * limit)}
              >
                {page}
              </button>
            </li>
          ) : (
            <li key={idx} className="page-item disabled">
              <span className="page-link">...</span>
            </li>
          )
        )}

        <li className={`page-item ${offset + limit >= total ? 'disabled' : ''}`}>
          <button
            className="page-link"
            onClick={() => onPageChange(offset + limit)}
            disabled={offset + limit >= total}
          >
            Next
          </button>
        </li>
      </ul>
    </nav>
  );
};
