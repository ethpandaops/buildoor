import React, { useState } from 'react';
import { authStore } from '../stores/authStore';

export type ArtifactFormat = 'json' | 'ssz';

// ArtifactUnauthorizedError marks a 401: the artifact belongs to a custom-built
// payload (or its envelope), which the backend only serves to logged-in users.
export class ArtifactUnauthorizedError extends Error {
  constructor() {
    super('log in to download (custom-built payload)');
    this.name = 'ArtifactUnauthorizedError';
  }
}

// downloadArtifact fetches a slot artifact with the login header and saves it
// through a Blob. The format picks the content negotiation: SSZ asks for the
// raw bytes, JSON for the beacon-API {version, data} wrapper.
export async function downloadArtifact(url: string, filename: string, format: ArtifactFormat): Promise<void> {
  const headers: Record<string, string> = {
    Accept: format === 'ssz' ? 'application/octet-stream' : 'application/json',
  };
  const token = await authStore.getAuthHeader();
  if (token) {
    headers['Authorization'] = `Bearer ${token}`;
  }

  const res = await fetch(url, { headers });
  if (res.status === 401) {
    throw new ArtifactUnauthorizedError();
  }
  if (!res.ok) {
    throw new Error(`artifact download failed (${res.status})`);
  }

  const blob = await res.blob();
  const objectUrl = URL.createObjectURL(blob);
  const anchor = document.createElement('a');
  anchor.href = objectUrl;
  anchor.download = format === 'json' ? filename.replace(/\.ssz$/, '') + '.json' : filename;
  document.body.appendChild(anchor);
  anchor.click();
  anchor.remove();
  URL.revokeObjectURL(objectUrl);
}

interface ArtifactButtonsProps {
  url: string;
  // SSZ filename (`.ssz`); the JSON download swaps the extension.
  filename: string;
  className?: string;
  // Receives non-auth failures; without it they render inline as well.
  onError?: (message: string) => void;
}

// ArtifactButtons renders the JSON / SSZ download pair of a slot artifact.
// Both go through an authenticated fetch, so custom-built payloads (served
// only to logged-in users) download when logged in and explain the 401
// otherwise.
export const ArtifactButtons: React.FC<ArtifactButtonsProps> = ({ url, filename, className, onError }) => {
  const [message, setMessage] = useState<string | null>(null);

  const download = (format: ArtifactFormat) => {
    setMessage(null);
    downloadArtifact(url, filename, format).catch((err) => {
      const text = err instanceof Error ? err.message : String(err);
      if (err instanceof ArtifactUnauthorizedError || !onError) {
        setMessage(text);
      } else {
        onError(text);
      }
    });
  };

  return (
    <span className={className ?? 'd-inline-flex flex-wrap align-items-center gap-1'}>
      <button type="button" className="btn btn-outline-secondary ap-artifact-btn" onClick={() => download('json')}>
        JSON
      </button>
      <button type="button" className="btn btn-outline-secondary ap-artifact-btn" onClick={() => download('ssz')}>
        SSZ
      </button>
      {message && (
        <span className="text-warning small">
          <i className="fas fa-lock me-1"></i>
          {message}
        </span>
      )}
    </span>
  );
};
