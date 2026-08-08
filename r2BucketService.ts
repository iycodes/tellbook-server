import {
	DeleteObjectCommand,
	GetObjectCommand,
	HeadObjectCommand,
	PutObjectCommand,
	S3Client
} from '@aws-sdk/client-s3';
import { getSignedUrl as getS3SignedUrl } from '@aws-sdk/s3-request-presigner';
import { createReadStream } from 'node:fs';
import { runtimeEnv } from '../config/runtimeEnv';

const R2_BUCKET = runtimeEnv.R2_PRIVATE_BUCKET_NAME?.trim();
const R2_PUBLIC_BUCKET = runtimeEnv.R2_PUBLIC_BUCKET_NAME?.trim();
const R2_ACCOUNT_ID = runtimeEnv.R2_ACCOUNT_ID?.trim();
const R2_ENDPOINT =
	runtimeEnv.R2_ENDPOINT?.trim() ||
	(R2_ACCOUNT_ID ? `https://${R2_ACCOUNT_ID}.r2.cloudflarestorage.com` : undefined);
const R2_ACCESS_KEY_ID = runtimeEnv.R2_ACCESS_KEY_ID?.trim();
const R2_SECRET_ACCESS_KEY = runtimeEnv.R2_SECRET_ACCESS_KEY?.trim();
const R2_PUBLIC_BUCKET_BASE_URL = runtimeEnv.R2_PUBLIC_BUCKET_BASE_URL?.trim();

function normalizeBaseUrl(url?: string | null) {
	if (!url) return null;
	const trimmed = url.trim();
	if (!trimmed) return null;
	return trimmed.replace(/\/+$/, '');
}

const r2EndpointBase = normalizeBaseUrl(R2_ENDPOINT);
const r2PublicBucketBase = normalizeBaseUrl(R2_PUBLIC_BUCKET_BASE_URL);

if (!R2_ENDPOINT || !R2_ACCESS_KEY_ID || !R2_SECRET_ACCESS_KEY) {
	throw new Error('R2 credentials are required');
}

const r2Client = new S3Client({
	region: 'auto',
	endpoint: R2_ENDPOINT,
	credentials: {
		accessKeyId: R2_ACCESS_KEY_ID,
		secretAccessKey: R2_SECRET_ACCESS_KEY
	}
});

export const clientBrandingBucket = R2_PUBLIC_BUCKET ? { name: R2_PUBLIC_BUCKET } : null;

function encodeObjectKey(key: string) {
	return encodeURI(key).replace(/\?/g, '%3F').replace(/#/g, '%23');
}

function requireBucketName(bucketName?: string) {
	const resolved = bucketName || R2_BUCKET;
	if (!resolved) throw new Error('R2_PRIVATE_BUCKET_NAME is required');
	return resolved;
}

function isPublicBucket(bucketName: string) {
	return Boolean(R2_PUBLIC_BUCKET && bucketName === R2_PUBLIC_BUCKET);
}

export function getStorageObjectUrl(objectKey: string, bucketName?: string) {
	const resolvedBucket = requireBucketName(bucketName);
	const encodedKey = encodeObjectKey(objectKey);

	if (isPublicBucket(resolvedBucket) && r2PublicBucketBase) {
		return `${r2PublicBucketBase}/${encodedKey}`;
	}
	if (!r2EndpointBase) throw new Error('R2 endpoint is not configured');
	return `${r2EndpointBase}/${resolvedBucket}/${encodedKey}`;
}

export async function uploadToR2(
	buffer: Buffer,
	filename: string,
	mimetype: string,
	bucketName?: string
) {
	const resolvedBucket = requireBucketName(bucketName);
	await r2Client.send(
		new PutObjectCommand({
			Bucket: resolvedBucket,
			Key: filename,
			Body: buffer,
			ContentType: mimetype
		})
	);
	return getStorageObjectUrl(filename, resolvedBucket);
}

export async function uploadFilePathToStorage(
	filePath: string,
	filename: string,
	mimetype: string,
	bucketName?: string
) {
	const resolvedBucket = requireBucketName(bucketName);
	await r2Client.send(
		new PutObjectCommand({
			Bucket: resolvedBucket,
			Key: filename,
			Body: createReadStream(filePath),
			ContentType: mimetype
		})
	);
	return getStorageObjectUrl(filename, resolvedBucket);
}

export async function objectExistsInStorage(objectKey: string, bucketName?: string) {
	const resolvedBucket = requireBucketName(bucketName);
	try {
		await r2Client.send(
			new HeadObjectCommand({
				Bucket: resolvedBucket,
				Key: objectKey
			})
		);
		return true;
	} catch (err: any) {
		const status = err?.$metadata?.httpStatusCode;
		if (status === 404 || err?.name === 'NotFound' || err?.Code === 'NotFound') {
			return false;
		}
		throw err;
	}
}

function parseR2Url(fullUrl: string) {
	try {
		const urlObj = new URL(fullUrl);
		const base = `${urlObj.protocol}//${urlObj.host}`;
		const path = urlObj.pathname.replace(/^\/+/, '');

		if (r2PublicBucketBase && fullUrl.startsWith(r2PublicBucketBase + '/')) {
			if (!R2_PUBLIC_BUCKET) return null;
			return {
				bucketName: R2_PUBLIC_BUCKET,
				objectKey: decodeURIComponent(fullUrl.slice(r2PublicBucketBase.length + 1))
			};
		}

		if (r2EndpointBase && base === r2EndpointBase) {
			const parts = path.split('/').filter(Boolean);
			if (parts.length < 2) return null;
			return {
				bucketName: parts[0],
				objectKey: decodeURIComponent(parts.slice(1).join('/'))
			};
		}

		if (urlObj.hostname.endsWith('.r2.dev')) {
			const bucketName = urlObj.hostname.split('.')[0];
			if (!bucketName || !path) return null;
			return { bucketName, objectKey: decodeURIComponent(path) };
		}

		return null;
	} catch {
		return null;
	}
}

export function parseStorageUrl(fullUrl: string) {
	return parseR2Url(fullUrl);
}

export function getDefaultStorageBucketName() {
	return requireBucketName();
}

export async function deleteFromR2(objectKey: string, bucketName?: string) {
	const resolvedBucket = requireBucketName(bucketName);
	await r2Client.send(
		new DeleteObjectCommand({
			Bucket: resolvedBucket,
			Key: objectKey
		})
	);
}

export async function signUrl(fullUrl: string, expiry?: number): Promise<string> {
	const parsed = parseR2Url(fullUrl);
	if (!parsed) {
		throw new Error('Expected R2 URL');
	}
	if (isPublicBucket(parsed.bucketName)) {
		return fullUrl;
	}
	const expiresAt = expiry || Date.now() + 15 * 60 * 1000;
	const expiresInSeconds = Math.max(1, Math.floor((expiresAt - Date.now()) / 1000));
	const command = new GetObjectCommand({ Bucket: parsed.bucketName, Key: parsed.objectKey });
	return getS3SignedUrl(r2Client, command, { expiresIn: expiresInSeconds });
}
