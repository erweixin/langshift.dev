'use client';

export function Logo() {
  return (
    <div
      className="flex cursor-default items-center gap-2"
      onClick={(e) => e.preventDefault()}
    >
      <svg
        width="24"
        height="24"
        xmlns="http://www.w3.org/2000/svg"
        aria-label="Logo"
      >
        <circle cx={12} cy={12} r={12} fill="currentColor" />
      </svg>
      Snekroll
    </div>
  );
}
