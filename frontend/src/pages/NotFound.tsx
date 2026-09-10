import { Button } from '@heroui/react';
import { ArrowLeft, Home } from 'lucide-react';
import { useNavigate } from 'react-router-dom';

export default function NotFound() {
    const navigate = useNavigate();
    return (
        <div className='flex min-h-[60dvh] items-center justify-center p-6'>
            <section aria-labelledby='not-found-title' className='max-w-lg text-center'>
                <p className='font-mono text-sm font-semibold text-muted'>404</p>
                <h1 className='mt-2 text-2xl font-semibold' id='not-found-title'>
                    Page not found
                </h1>
                <p className='mt-2 text-sm leading-6 text-muted'>
                    The address may be outdated, or you may not have access to this resource.
                </p>
                <div className='mt-6 flex flex-wrap justify-center gap-2'>
                    <Button variant='secondary' onPress={() => navigate(-1)}>
                        <ArrowLeft aria-hidden='true' className='h-4 w-4' />
                        Go back
                    </Button>
                    <Button onPress={() => navigate('/')}>
                        <Home aria-hidden='true' className='h-4 w-4' />
                        Dashboard
                    </Button>
                </div>
            </section>
        </div>
    );
}
